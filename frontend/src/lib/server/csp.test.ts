import { afterEach, describe, expect, it, vi } from 'vitest';
import { isfcOrigins, widenCsp, withExtraOrigins } from './csp';

const BASE =
	"default-src 'self'; script-src 'self' 'nonce-abc'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'";

describe('withExtraOrigins', () => {
	it.each([
		['no origins: unchanged', BASE, [], BASE],
		[
			'adds the origin to connect-src and form-action only',
			BASE,
			['https://auth.isfc.example'],
			"default-src 'self'; script-src 'self' 'nonce-abc'; connect-src 'self' https://auth.isfc.example; form-action 'self' https://auth.isfc.example; frame-ancestors 'none'"
		],
		[
			'does not repeat an origin already there',
			"connect-src 'self' https://a.example; form-action 'self'",
			['https://a.example'],
			"connect-src 'self' https://a.example; form-action 'self' https://a.example"
		],
		[
			'a policy without those directives is left alone',
			"default-src 'self'",
			['https://a.example'],
			"default-src 'self'"
		]
	])('%s', (_name, csp, origins, want) => {
		expect(withExtraOrigins(csp, origins)).toBe(want);
	});
});

vi.mock('$lib/server/auth', () => ({ authMode: vi.fn() }));
vi.mock('$lib/server/auth/config', () => ({ readIsfcConfig: vi.fn() }));
const { authMode } = await import('$lib/server/auth');
const { readIsfcConfig } = await import('$lib/server/auth/config');

afterEach(() => vi.resetAllMocks());

describe('isfcOrigins', () => {
	it.each([
		['dev mode', 'dev', { ok: true, config: { authUrl: 'https://x.example' } }, []],
		[
			'isfc mode',
			'isfc',
			{ ok: true, config: { authUrl: 'https://auth.isfc.example/base' } },
			['https://auth.isfc.example']
		],
		['isfc mode, missing setting', 'isfc', { ok: false, missing: ['HUB_ISFC_AUTH_URL'] }, []],
		['no mode', null, { ok: true, config: { authUrl: 'https://x.example' } }, []]
	])('%s', (_n, mode, cfg, want) => {
		vi.mocked(authMode).mockReturnValue(mode as never);
		vi.mocked(readIsfcConfig).mockReturnValue(cfg as never);
		expect(isfcOrigins()).toEqual(want);
	});
});

describe('widenCsp', () => {
	const run = async (csp: string | null) => {
		const headers = new Headers(csp ? { 'content-security-policy': csp } : {});
		const resolve = vi.fn(async () => new Response('ok', { headers }));
		return widenCsp({ event: {} as never, resolve });
	};

	it('widens the header in ISFC mode', async () => {
		vi.mocked(authMode).mockReturnValue('isfc');
		vi.mocked(readIsfcConfig).mockReturnValue({
			ok: true,
			config: { authUrl: 'https://auth.isfc.example' }
		} as never);
		const res = await run(BASE);
		expect(res.headers.get('content-security-policy')).toContain(
			"connect-src 'self' https://auth.isfc.example"
		);
	});

	it('leaves responses without a CSP alone (data requests, redirects)', async () => {
		vi.mocked(authMode).mockReturnValue('isfc');
		const res = await run(null);
		expect(res.headers.get('content-security-policy')).toBeNull();
	});
});

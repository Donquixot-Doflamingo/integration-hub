import { isRedirect, type RequestEvent } from '@sveltejs/kit';
import { describe, expect, it, vi } from 'vitest';
import { devProvider } from './dev';
import { authMode, authProvider, resolveAuthMode, sessionSecret } from './index';
import { decodeSession, encodeSession, newSession, toPublicSession, type Session } from './session';
import { attachAdminToken, authenticate } from './hooks';
import { load as appLayoutLoad } from '../../../routes/(app)/+layout.server';
import { actions, load as loginLoad } from '../../../routes/(auth)/login/+page.server';

const SECRET = 'x'.repeat(40);
const form = (o: Record<string, string | undefined>) => {
	const f = new FormData();
	for (const [k, v] of Object.entries(o)) if (v !== undefined) f.set(k, v);
	return f;
};
const session = (over: Partial<Session> = {}): Session => ({
	...newSession({
		provider: 'dev',
		user: { subject: 'dev|a@b.c', name: 'A', email: 'a@b.c', roles: ['editor'], tenants: [] },
		accessToken: null
	}),
	...over
});

describe('session cookie', () => {
	it('round-trips', () => {
		const s = session();
		expect(decodeSession(encodeSession(s, SECRET), SECRET)).toEqual(s);
	});

	it.each([
		{ name: 'missing', value: () => undefined },
		{ name: 'wrong secret', value: () => encodeSession(session(), 'y'.repeat(40)) },
		{
			name: 'tampered ciphertext (role upgrade attempt)',
			value: () => {
				const v = encodeSession(session(), SECRET);
				const raw = Buffer.from(v, 'base64url');
				raw[raw.length - 3]! ^= 0xff;
				return raw.toString('base64url');
			}
		},
		{
			name: 'a plain base64 JSON payload (the old signed format)',
			value: () =>
				Buffer.from(
					JSON.stringify({ ...session(), user: { ...session().user, roles: ['admin'] } })
				).toString('base64url')
		},
		{ name: 'expired', value: () => encodeSession(session({ exp: 1 }), SECRET) },
		{ name: 'garbage', value: () => 'not-a-cookie' }
	])('rejects $name', ({ value }) => {
		expect(decodeSession(value(), SECRET)).toBeNull();
	});

	it('never exposes the token to the browser', () => {
		const pub = toPublicSession(session({ accessToken: 'secret-token' }));
		expect(JSON.stringify(pub)).not.toContain('secret-token');
	});
});

describe('dev provider', () => {
	it.each(['admin', 'editor', 'viewer'] as const)('signs in as %s', async (role) => {
		const r = await devProvider.signIn(form({ role, name: 'Priya', email: 'p@example.com' }));
		expect(r).toMatchObject({
			ok: true,
			session: { provider: 'dev', user: { roles: [role], tenants: [] } }
		});
	});

	it('signs in a tenant viewer with tenants', async () => {
		const r = await devProvider.signIn(
			form({ role: 'tenant_viewer', tenants: 'orders-service, billing_svc' })
		);
		expect(r).toMatchObject({
			ok: true,
			session: { user: { tenants: ['orders-service', 'billing_svc'] } }
		});
	});

	it.each([
		{ name: 'no role', f: {}, field: 'role' },
		{ name: 'unknown role', f: { role: 'root' }, field: 'role' },
		{
			name: 'tenant viewer without tenants',
			f: { role: 'tenant_viewer', tenants: '' },
			field: 'tenants'
		},
		{ name: 'bad tenant id', f: { role: 'tenant_viewer', tenants: 'a b!' }, field: 'tenants' },
		{ name: 'bad email', f: { role: 'viewer', email: 'nope' }, field: 'email' }
	])('rejects $name', async ({ f, field }) => {
		expect(await devProvider.signIn(form(f))).toMatchObject({ ok: false, status: 400, field });
	});
});

describe('auth mode (HUB-495)', () => {
	const r = (mode: string | undefined, over: { mocking?: boolean; nodeEnv?: string } = {}) =>
		resolveAuthMode({ mocking: false, nodeEnv: 'production', mode, ...over });

	it('isfc is the real login', () => {
		expect(r('isfc')).toEqual({ mode: 'isfc' });
	});

	it('dev is refused outside mock builds and tests', () => {
		expect(r('dev')).toMatchObject({
			mode: null,
			reason: expect.stringMatching(/only allowed in mock builds and tests/)
		});
		expect(r('dev', { nodeEnv: 'development' })).toMatchObject({ mode: null });
		expect(r('dev', { nodeEnv: undefined })).toMatchObject({ mode: null });
	});

	it('dev is allowed in mock builds and when NODE_ENV=test', () => {
		expect(r('dev', { mocking: true })).toEqual({ mode: 'dev' });
		expect(r(undefined, { mocking: true })).toEqual({ mode: 'dev' });
		expect(r('dev', { nodeEnv: 'test' })).toEqual({ mode: 'dev' });
	});

	it('an unset or unknown mode is refused (it must be chosen on purpose)', () => {
		expect(r(undefined)).toMatchObject({ mode: null, reason: expect.stringMatching(/not set/) });
		expect(r('')).toMatchObject({ mode: null });
		expect(r('oidc')).toMatchObject({ mode: null, reason: expect.stringMatching(/not a mode/) });
	});

	it('mock builds always use dev, whatever the setting: there is no identity provider to call', () => {
		expect(r('isfc', { mocking: true })).toEqual({ mode: 'dev' });
	});
});

describe('this test run (NODE_ENV=test, no PUBLIC_AUTH_MODE)', () => {
	it('refuses sign-in: the mode is unset', async () => {
		expect(authMode()).toBeNull();
		const p = authProvider();
		expect(p.id).toBe('none');
		expect(await p.describe()).toMatchObject({
			provider: 'none',
			ready: false,
			missing: ['PUBLIC_AUTH_MODE']
		});
		expect(await p.signIn(form({}))).toMatchObject({ ok: false, status: 503 });
		expect(sessionSecret()).toBeNull();
	});
});

function cookieJar(initial: Record<string, string> = {}) {
	const jar = new Map(Object.entries(initial));
	return {
		jar,
		get: (k: string) => jar.get(k),
		set: vi.fn((k: string, v: string) => void jar.set(k, v)),
		delete: vi.fn((k: string) => void jar.delete(k))
	};
}

describe('guards', () => {
	it('redirects unauthenticated users to login with the page they asked for', () => {
		const event = { locals: { session: null }, url: new URL('http://c.test/dlq?tenant_id=a') };
		try {
			appLayoutLoad(event as never);
			expect.unreachable();
		} catch (e) {
			expect(isRedirect(e)).toBe(true);
			expect(e).toMatchObject({ status: 303, location: '/login?next=%2Fdlq%3Ftenant_id%3Da' });
		}
	});

	it('gives the page the token-free session', () => {
		const s = session({ accessToken: 'tok' });
		const out = appLayoutLoad({ locals: { session: s }, url: new URL('http://c.test/') } as never);
		expect(out).toEqual({
			session: toPublicSession(s),
			isfcMode: false,
			isfcAdminUrl: null
		});
		expect(JSON.stringify(out)).not.toContain('tok');
	});

	it('authenticate drops a cookie that does not verify and says the session ended', async () => {
		const cookies = cookieJar({ hub_console_session: 'forged.value' });
		const event = { cookies, locals: {} } as unknown as RequestEvent;
		await authenticate({ event, resolve: async () => new Response('ok') });
		expect(event.locals.session).toBeNull();
		expect(event.locals.sessionEnded).toBe(true);
		expect(cookies.delete).toHaveBeenCalledWith('hub_console_session', { path: '/' });
	});

	it('handleFetch adds the bearer token only to admin API requests', async () => {
		const seen: Request[] = [];
		const fetch = vi.fn(async (r: RequestInfo | URL) => {
			seen.push(r as Request);
			return new Response('{}');
		});
		const event = {
			locals: { session: session({ accessToken: 'tok' }) },
			url: new URL('http://c.test/x')
		};
		await attachAdminToken({
			event: event as never,
			request: new Request('http://c.test/admin/v1/me'),
			fetch
		});
		await attachAdminToken({
			event: event as never,
			request: new Request('https://elsewhere.example/admin/v1/me'),
			fetch
		});
		expect(seen[0]!.headers.get('Authorization')).toBe('Bearer tok');
		expect(seen[1]!.headers.get('Authorization')).toBeNull();
	});
});

describe('login page (no mode, no secret)', () => {
	it('reports sign-in unavailable with the missing settings', async () => {
		const out = await loginLoad({
			url: new URL('http://c.test/login?next=//evil'),
			locals: { session: null, sessionEnded: false }
		} as never);
		expect(out).toMatchObject({
			login: { provider: 'none', ready: false },
			next: '/',
			expired: false,
			error: null
		});
		expect((out as { login: { missing: string[] } }).login.missing).toEqual([
			'PUBLIC_AUTH_MODE',
			'HUB_SESSION_SECRET'
		]);
	});

	it('refuses to sign in without a session secret', async () => {
		const request = new Request('http://c.test/login', {
			method: 'POST',
			body: form({ role: 'admin' })
		});
		const cookies = cookieJar();
		const r = await actions.dev!({
			request,
			cookies,
			url: new URL('http://c.test/login')
		} as never);
		expect(r).toMatchObject({ status: 503 });
		expect(cookies.set).not.toHaveBeenCalled();
	});
});

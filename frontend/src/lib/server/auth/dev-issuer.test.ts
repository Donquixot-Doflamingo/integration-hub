import { describe, expect, it, vi } from 'vitest';
import { createDevProvider, mintDevToken } from './dev';
import { SESSION_TTL_SECONDS } from './session';

// HUB-286: against a running Hub, dev sign-in asks the admin's dev OIDC issuer for a token.
const ISSUER = 'http://127.0.0.1:8081';
const TOKEN_URL = `${ISSUER}/oidc/dev-token`;

function form(fields: Record<string, string>) {
	const f = new FormData();
	for (const [k, v] of Object.entries(fields)) f.set(k, v);
	return f;
}

function issuer(
	token: Response | (() => Response) = Response.json({ access_token: 'tok.en.sig' })
) {
	return vi.fn<typeof fetch>(async (input) => {
		const url = String(input);
		if (url === `${ISSUER}/.well-known/openid-configuration`) {
			return Response.json({ issuer: ISSUER, token_endpoint: TOKEN_URL });
		}
		if (url === TOKEN_URL) return typeof token === 'function' ? token() : token;
		return new Response('not found', { status: 404 });
	});
}

describe('createDevProvider with an issuer', () => {
	it('mints a token for the chosen identity and keeps it in the session', async () => {
		const f = issuer();
		const p = createDevProvider({ issuer: `${ISSUER}/`, fetch: f });
		expect(p.describe().notice).toMatch(/dev issuer signs your token/);
		const out = await p.signIn(
			form({
				role: 'tenant_viewer',
				tenants: 'orders-service, billing-svc',
				name: 'Tia',
				email: 'tia@example.com'
			})
		);
		expect(out).toMatchObject({
			ok: true,
			session: {
				provider: 'dev',
				accessToken: 'tok.en.sig',
				user: { roles: ['tenant_viewer'], tenants: ['orders-service', 'billing-svc'] }
			}
		});
		expect(f).toHaveBeenCalledTimes(2);
		const [url, init] = f.mock.calls[1]!;
		expect(url).toBe(TOKEN_URL);
		expect(init!.method).toBe('POST');
		expect(JSON.parse(init!.body as string)).toEqual({
			sub: 'dev|tia@example.com',
			email: 'tia@example.com',
			name: 'Tia',
			roles: ['tenant_viewer'],
			tenants: ['orders-service', 'billing-svc'],
			ttl_s: SESSION_TTL_SECONDS
		});
	});

	it('validates the form before calling the issuer', async () => {
		const f = issuer();
		const out = await createDevProvider({ issuer: ISSUER, fetch: f }).signIn(
			form({ role: 'superuser' })
		);
		expect(out).toMatchObject({ ok: false, status: 400, field: 'role' });
		expect(f).not.toHaveBeenCalled();
	});

	it.each([
		[
			'issuer down',
			vi.fn<typeof fetch>(async () => {
				throw new TypeError('fetch failed');
			}),
			/did not answer\. Is the admin role running with -admin-dev-oidc\?/
		],
		[
			'no discovery document',
			vi.fn<typeof fetch>(async () => new Response('nope', { status: 404 })),
			/did not answer/
		],
		[
			'token refused',
			issuer(Response.json({ error: 'bad role' }, { status: 400 })),
			/refused the sign-in \(HTTP 400\)/
		],
		[
			'token not JSON',
			issuer(new Response('<html>', { status: 200 })),
			/refused the sign-in \(HTTP 200\)/
		]
	])('%s: sign-in fails with 503 and says why', async (_name, f, message) => {
		const out = await createDevProvider({ issuer: ISSUER, fetch: f }).signIn(
			form({ role: 'admin' })
		);
		expect(out).toMatchObject({ ok: false, status: 503 });
		expect((out as { message: string }).message).toMatch(message);
	});
});

describe('createDevProvider without an issuer (mock mode)', () => {
	it('signs in with no token and no network call', async () => {
		const f = vi.fn<typeof fetch>();
		const out = await createDevProvider({ fetch: f }).signIn(form({ role: 'editor' }));
		expect(out).toMatchObject({
			ok: true,
			session: { accessToken: null, user: { roles: ['editor'] } }
		});
		expect(f).not.toHaveBeenCalled();
	});
});

describe('mintDevToken', () => {
	it('never puts the token in an error message', async () => {
		const out = await mintDevToken(
			ISSUER,
			{ sub: 's', email: '', name: 'n', roles: ['admin'], tenants: [], ttl_s: 60 },
			issuer(Response.json({ token: 'leaky' }, { status: 500 }))
		);
		expect(out.ok).toBe(false);
		expect(JSON.stringify(out)).not.toContain('leaky');
	});
});

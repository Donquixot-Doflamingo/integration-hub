import { isRedirect, type RequestEvent } from '@sveltejs/kit';
import { describe, expect, it, vi } from 'vitest';
import { devProvider } from './dev';
import { authMode, authProvider, sessionSecret } from './index';
import { oidcProvider } from './oidc';
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
		{ name: 'no signature', value: () => encodeSession(session(), SECRET).split('.')[0] },
		{ name: 'wrong secret', value: () => encodeSession(session(), 'y'.repeat(40)) },
		{
			name: 'tampered payload (role upgrade)',
			value: () => {
				const [, sig] = encodeSession(session(), SECRET).split('.');
				const forged = Buffer.from(
					JSON.stringify({ ...session(), user: { ...session().user, roles: ['admin'] } })
				).toString('base64url');
				return `${forged}.${sig}`;
			}
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

describe('oidc placeholder', () => {
	it('lists missing settings and refuses sign-in', async () => {
		const p = oidcProvider({ issuer: 'https://idp.example' });
		expect(p.describe()).toMatchObject({
			provider: 'oidc',
			ready: false,
			missing: ['HUB_OIDC_CLIENT_ID', 'HUB_OIDC_REDIRECT_URI']
		});
		expect(await p.signIn(form({}))).toMatchObject({ ok: false, status: 503 });
	});

	it('is the default outside mock mode, and has no session secret without config', () => {
		expect(authMode()).toBe('oidc');
		expect(authProvider().id).toBe('oidc');
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
		expect(out).toEqual({ session: toPublicSession(s) });
	});

	it('authenticate drops a cookie that does not verify', async () => {
		const cookies = cookieJar({ hub_console_session: 'forged.value' });
		const event = { cookies, locals: {} } as unknown as RequestEvent;
		await authenticate({ event, resolve: async () => new Response('ok') });
		expect(event.locals.session).toBeNull();
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

describe('login page (OIDC placeholder, no secret)', () => {
	it('reports sign-in unavailable with the missing settings', () => {
		const out = loginLoad({
			url: new URL('http://c.test/login?next=//evil'),
			locals: { session: null }
		} as never);
		expect(out).toMatchObject({
			login: { provider: 'oidc', ready: false },
			next: '/',
			expired: false
		});
		expect((out as { login: { missing: string[] } }).login.missing).toContain('HUB_SESSION_SECRET');
	});

	it('refuses to sign in without a session secret', async () => {
		const request = new Request('http://c.test/login', {
			method: 'POST',
			body: form({ role: 'admin' })
		});
		const cookies = cookieJar();
		const r = await actions.default!({
			request,
			cookies,
			url: new URL('http://c.test/login')
		} as never);
		expect(r).toMatchObject({ status: 503 });
		expect(cookies.set).not.toHaveBeenCalled();
	});
});

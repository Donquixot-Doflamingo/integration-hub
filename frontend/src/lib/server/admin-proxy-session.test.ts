import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AUTH, createFakeIsfc, tokenResponse } from '$lib/testing/fake-isfc';
import { decodeSession, newSession } from './auth/session';

/** The session value the browser would hold after this response: the Set-Cookie headers, joined. */
function sessionFromHeaders(res: Response): ReturnType<typeof decodeSession> {
	const chunks = new Map<string, string>();
	for (const h of res.headers.getSetCookie()) {
		const [pair] = h.split(';');
		const [k, ...v] = pair!.split('=');
		if (k!.startsWith('hub_console_session') && v.join('=')) chunks.set(k!, v.join('='));
	}
	const value = [...chunks.keys()]
		.sort()
		.map((k) => chunks.get(k))
		.join('');
	return decodeSession(value, SECRET);
}

// HUB-492 / HUB-494: the proxy's Handle keeps the session cookie in step with what the gateway says.
const SECRET = 's'.repeat(40);
const env = vi.hoisted(() => ({
	priv: {} as Record<string, string | undefined>,
	pub: {} as Record<string, string | undefined>
}));
vi.mock('$env/dynamic/private', () => ({ env: env.priv }));
vi.mock('$env/dynamic/public', () => ({ env: env.pub }));

const { proxyAdminApi } = await import('./admin-proxy');

const GW = 'https://gw.isfc.test/hub';
const ECHO = '11111111-1111-4111-8111-111111111111';

beforeEach(() => {
	for (const k of Object.keys(env.priv)) delete env.priv[k];
	Object.assign(env.priv, {
		HUB_ADMIN_UPSTREAM: GW,
		HUB_ISFC_AUTH_URL: AUTH,
		HUB_SESSION_SECRET: SECRET
	});
	env.pub.PUBLIC_AUTH_MODE = 'isfc';
});

function setup(tenantId: string | null, fetchImpl: typeof fetch) {
	const store = new Map<string, string>();
	const cookies = {
		get: (k: string) => store.get(k),
		set: (k: string, v: string) => void store.set(k, v),
		delete: (k: string) => void store.delete(k),
		serialize: (k: string, v: string, o: { maxAge?: number }) =>
			`${k}=${v}; Max-Age=${o.maxAge ?? ''}; Path=/`
	};
	const url = new URL('https://console.test/admin/v1/me');
	const event = {
		request: new Request(url, { headers: { 'user-agent': 'UA/1' } }),
		url,
		isSubRequest: false,
		cookies,
		fetch: fetchImpl,
		locals: {
			sessionEnded: false,
			session: newSession({
				provider: 'isfc',
				user: { subject: 'u', name: null, email: null, roles: [], tenants: [] },
				accessToken: 'access-old',
				refreshToken: 'refresh-old',
				isfcSessionId: 's',
				accessExp: Math.floor(Date.now() / 1000) + 600,
				deviceId: 'device-00000000-0000-4000-8000-000000000001',
				tenantId
			})
		}
	};
	return { event, cookies };
}

describe('proxyAdminApi with an ISFC session', () => {
	it('stores the tenant the gateway fell back to in the session cookie', async () => {
		vi.stubGlobal(
			'fetch',
			vi.fn(async () => Response.json({}, { headers: { 'x-isfc-tenant-id': ECHO } }))
		);
		const { event, cookies } = setup(null, fetch);
		const res = (await proxyAdminApi({
			event: event as never,
			resolve: async () => new Response()
		})) as Response;
		expect(res.status).toBe(200);
		// The proxy answers with its own Response, which SvelteKit does not add cookies to: the
		// new session must be on the response itself (found by a smoke run against a real server).
		expect(sessionFromHeaders(res)).toMatchObject({ tenantId: ECHO, accessToken: 'access-old' });
		expect(event.locals.session.tenantId).toBe(ECHO);
		void cookies;
	});

	it('refreshes after a GW_004, repeats the call with the new token and keeps the rotated tokens in the cookie', async () => {
		const isfc = createFakeIsfc({
			token: () => tokenResponse({ access_token: 'new.access.token', refresh_token: 'refresh-new' })
		});
		const seen: (string | null)[] = [];
		vi.stubGlobal(
			'fetch',
			vi.fn(async (u: RequestInfo | URL, init?: RequestInit) => {
				if (String(u).startsWith(GW)) {
					seen.push(new Headers(init?.headers).get('authorization'));
					return seen.length === 1
						? Response.json({ error_code: 'GW_004', error: 'bad token' }, { status: 401 })
						: Response.json({ ok: true });
				}
				return isfc.fetch(u, init);
			})
		);
		const { event, cookies } = setup(null, fetch);
		const res = (await proxyAdminApi({
			event: event as never,
			resolve: async () => new Response()
		})) as Response;
		expect(res.status).toBe(200);
		expect(seen).toEqual(['Bearer access-old', 'Bearer new.access.token']);
		// The rotated tokens reach the browser on the proxied response itself.
		expect(sessionFromHeaders(res)).toMatchObject({
			accessToken: 'new.access.token',
			refreshToken: 'refresh-new'
		});
		void cookies;
	});

	it('GW_007 (session ended at ISFC) reaches the console as UNAUTHORIZED, and nothing is refreshed', async () => {
		const isfc = createFakeIsfc();
		vi.stubGlobal(
			'fetch',
			vi.fn(async (u: RequestInfo | URL, init?: RequestInit) =>
				String(u).startsWith(GW)
					? Response.json({ error_code: 'GW_007', error: 'inactive' }, { status: 401 })
					: isfc.fetch(u, init)
			)
		);
		const { event } = setup(null, fetch);
		const res = (await proxyAdminApi({
			event: event as never,
			resolve: async () => new Response()
		})) as Response;
		expect(res.status).toBe(401);
		expect(await res.json()).toMatchObject({ error: { code: 'UNAUTHORIZED' } });
		expect(isfc.calls).toHaveLength(0);
		// Nothing changed in the session, so no cookie is sent.
		expect(res.headers.getSetCookie()).toEqual([]);
	});
});

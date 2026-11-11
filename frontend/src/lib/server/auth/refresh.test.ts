import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AUTH, createFakeIsfc, isfcError, jwt, tokenResponse } from '$lib/testing/fake-isfc';
import type { IsfcConfig } from './config';
import { decodeSession, newSession, type Session } from './session';
import { readSessionCookie } from './cookie';
import { REFRESH_RETRY_MS, refreshTokens } from './isfc';
import {
	needsRefresh,
	refreshForEvent,
	refreshSession,
	REFRESH_SKEW_SECONDS,
	resetRefreshState
} from './refresh';
import { authenticate } from './hooks';
import { load as appLayoutLoad } from '../../../routes/(app)/+layout.server';

// HUB-492: single-flight refresh with a 409 retry after 300 ms, expiry -> /login?expired=1.
const SECRET = 's'.repeat(40);
const env = vi.hoisted(() => ({
	priv: {} as Record<string, string | undefined>,
	pub: {} as Record<string, string | undefined>
}));
vi.mock('$env/dynamic/private', () => ({ env: env.priv }));
vi.mock('$env/dynamic/public', () => ({ env: env.pub }));

const DEVICE = 'device-00000000-0000-4000-8000-000000000009';
const NOW = Date.parse('2026-11-11T10:00:00Z');
const config: IsfcConfig = {
	authUrl: AUTH,
	clientId: 'integration-hub-console',
	redirectUri: null,
	gatewayUrl: null,
	platformTenantId: null
};

const isfcSession = (over: Partial<Session> = {}): Session =>
	newSession(
		{
			provider: 'isfc',
			user: { subject: 'u1', name: 'P', email: null, roles: [], tenants: [] },
			accessToken: 'access-old',
			refreshToken: 'refresh-old',
			isfcSessionId: 'sess-1',
			accessExp: NOW / 1000 + 600,
			deviceId: DEVICE,
			...over
		},
		NOW
	);

const deps = (fetchImpl: typeof fetch) => ({
	config,
	fetch: fetchImpl,
	origin: 'https://console.test',
	userAgent: 'UA/1',
	now: NOW,
	sleep: async () => {}
});

beforeEach(() => {
	resetRefreshState();
	for (const k of Object.keys(env.priv)) delete env.priv[k];
	Object.assign(env.priv, { HUB_ISFC_AUTH_URL: AUTH, HUB_SESSION_SECRET: SECRET });
	env.pub.PUBLIC_AUTH_MODE = 'isfc';
});

describe('needsRefresh', () => {
	it('is due in the last minute before the access token expires, and after', () => {
		const at = (left: number) => isfcSession({ accessExp: NOW / 1000 + left });
		expect(REFRESH_SKEW_SECONDS).toBe(60);
		expect(needsRefresh(at(120), NOW)).toBe(false);
		expect(needsRefresh(at(59), NOW)).toBe(true);
		expect(needsRefresh(at(-5), NOW)).toBe(true);
	});

	it('is never due for the dev provider or a session without a refresh token', () => {
		expect(needsRefresh(isfcSession({ provider: 'dev', accessExp: 1 }), NOW)).toBe(false);
		expect(needsRefresh(isfcSession({ refreshToken: null, accessExp: 1 }), NOW)).toBe(false);
		expect(needsRefresh(isfcSession({ accessExp: null }), NOW)).toBe(false);
	});
});

describe('refreshTokens (409 retry)', () => {
	const input = {
		refreshToken: 'r',
		sessionId: 's',
		redirectUri: 'https://console.test/auth/callback',
		deviceId: DEVICE,
		userAgent: 'UA/1'
	};

	it('sends the refresh grant with the device id, and the rotated tokens come back', async () => {
		const isfc = createFakeIsfc({
			token: () => tokenResponse({ access_token: jwt({ sub: 'u' }), refresh_token: 'r2' })
		});
		const t = await refreshTokens(config, isfc.fetch, input, { now: NOW });
		expect(t.refreshToken).toBe('r2');
		const c = isfc.to('/oauth2/token')[0]!;
		expect(c.headers.get('x-device-id')).toBe(DEVICE);
		expect(c.body).toEqual({
			grant_type: 'refresh_token',
			client_id: 'integration-hub-console',
			redirect_uri: input.redirectUri,
			refresh_token: 'r',
			session_id: 's',
			session_info: { platform: 'WEB', login_method: 'refresh' }
		});
	});

	it('retries a 409 once, after 300 ms', async () => {
		const sleep = vi.fn(async () => {});
		const isfc = createFakeIsfc({
			token: (_c, n) =>
				n === 1 ? isfcError(409, 'AS_FB_20') : tokenResponse({ refresh_token: 'r2' })
		});
		const t = await refreshTokens(config, isfc.fetch, input, { now: NOW, sleep });
		expect(REFRESH_RETRY_MS).toBe(300);
		expect(sleep).toHaveBeenCalledExactlyOnceWith(300);
		expect(isfc.to('/oauth2/token')).toHaveLength(2);
		expect(t.refreshToken).toBe('r2');
	});

	it('does not retry a second 409, nor any other error', async () => {
		const sleep = vi.fn(async () => {});
		const twice = createFakeIsfc({ token: () => isfcError(409, 'AS_FB_20') });
		await expect(refreshTokens(config, twice.fetch, input, { sleep })).rejects.toMatchObject({
			status: 409
		});
		expect(twice.to('/oauth2/token')).toHaveLength(2);
		const four = createFakeIsfc({ token: () => isfcError(401, 'AS_FB_01') });
		await expect(refreshTokens(config, four.fetch, input, { sleep })).rejects.toMatchObject({
			status: 401
		});
		expect(four.to('/oauth2/token')).toHaveLength(1);
	});
});

describe('refreshSession (single flight)', () => {
	it('ten concurrent refreshes with the same token make one call to ISFC', async () => {
		let release!: () => void;
		const gate = new Promise<void>((r) => (release = r));
		const isfc = createFakeIsfc({
			token: async () => {
				await gate;
				return tokenResponse({ refresh_token: 'refresh-new', session_id: 'sess-1' });
			}
		});
		const s = isfcSession();
		const all = Array.from({ length: 10 }, () => refreshSession(s, deps(isfc.fetch)));
		release();
		const results = await Promise.all(all);
		expect(isfc.to('/oauth2/token')).toHaveLength(1);
		for (const r of results) {
			expect(r).toMatchObject({ ok: true, session: { refreshToken: 'refresh-new' } });
		}
	});

	it('a late request that still carries the old token gets the same answer, not a second call', async () => {
		const isfc = createFakeIsfc({ token: () => tokenResponse({ refresh_token: 'refresh-new' }) });
		const s = isfcSession();
		const first = await refreshSession(s, deps(isfc.fetch));
		const late = await refreshSession(s, { ...deps(isfc.fetch), now: NOW + 5_000 });
		expect(late).toEqual(first);
		expect(isfc.to('/oauth2/token')).toHaveLength(1);
	});

	it('after 30 s it asks ISFC again', async () => {
		const isfc = createFakeIsfc();
		const s = isfcSession();
		await refreshSession(s, deps(isfc.fetch));
		await refreshSession(s, { ...deps(isfc.fetch), now: NOW + 31_000 });
		expect(isfc.to('/oauth2/token')).toHaveLength(2);
	});

	it('different sessions refresh independently', async () => {
		const isfc = createFakeIsfc();
		await Promise.all([
			refreshSession(isfcSession({ refreshToken: 'a' }), deps(isfc.fetch)),
			refreshSession(isfcSession({ refreshToken: 'b' }), deps(isfc.fetch))
		]);
		expect(isfc.to('/oauth2/token')).toHaveLength(2);
	});

	it('ISFC refusing the refresh token ends the session; a down ISFC does not', async () => {
		const refused = createFakeIsfc({ token: () => isfcError(401, 'AS_FB_01') });
		expect(await refreshSession(isfcSession({ refreshToken: 'x1' }), deps(refused.fetch))).toEqual({
			ok: false,
			ended: true
		});
		const down = createFakeIsfc({ token: () => new Response('x', { status: 503 }) });
		expect(await refreshSession(isfcSession({ refreshToken: 'x2' }), deps(down.fetch))).toEqual({
			ok: false,
			ended: false
		});
		const net = (async () => {
			throw new TypeError('fetch failed');
		}) as never;
		expect(await refreshSession(isfcSession({ refreshToken: 'x3' }), deps(net))).toEqual({
			ok: false,
			ended: false
		});
	});

	it('a transient failure is not replayed: the next request tries again', async () => {
		let n = 0;
		const isfc = createFakeIsfc({
			token: () => (++n === 1 ? new Response('x', { status: 503 }) : tokenResponse())
		});
		const s = isfcSession({ refreshToken: 'x4' });
		expect(await refreshSession(s, deps(isfc.fetch))).toMatchObject({ ok: false, ended: false });
		expect(await refreshSession(s, deps(isfc.fetch))).toMatchObject({ ok: true });
	});

	it('keeps the tenant and device of the session', async () => {
		const isfc = createFakeIsfc();
		const r = await refreshSession(
			isfcSession({ tenantId: 't-1', refreshToken: 'x5' }),
			deps(isfc.fetch)
		);
		expect(r).toMatchObject({
			ok: true,
			session: { tenantId: 't-1', deviceId: DEVICE, isfcSessionId: 'sess-1' }
		});
	});
});

function eventFor(session: Session | null, fetchImpl: typeof fetch, cookieValue?: string) {
	const store = new Map<string, string>();
	if (cookieValue) store.set('hub_console_session', cookieValue);
	const sets: { name: string; value: string }[] = [];
	const deleted: string[] = [];
	return {
		sets,
		deleted,
		store,
		event: {
			cookies: {
				get: (k: string) => store.get(k),
				set: (name: string, value: string) => {
					store.set(name, value);
					sets.push({ name, value });
				},
				delete: (name: string) => {
					store.delete(name);
					deleted.push(name);
				}
			},
			locals: { session, sessionEnded: false },
			request: new Request('https://console.test/tenants', { headers: { 'user-agent': 'UA/1' } }),
			url: new URL('https://console.test/tenants'),
			fetch: fetchImpl
		}
	};
}

describe('refreshForEvent / authenticate', () => {
	it('refreshes a session about to expire and sets the new encrypted cookie', async () => {
		const isfc = createFakeIsfc({
			token: () =>
				tokenResponse({
					access_token: jwt({ sub: 'u1', exp: 4102444800 }),
					refresh_token: 'refresh-2'
				})
		});
		const near = isfcSession({ accessExp: Math.floor(Date.now() / 1000) + 10 });
		const e = eventFor(near, isfc.fetch);
		expect(await refreshForEvent(e.event as never, { fetch: isfc.fetch })).toBe('refreshed');
		expect(e.event.locals.session).toMatchObject({ refreshToken: 'refresh-2' });
		const stored = decodeSession(readSessionCookie(e.event.cookies), SECRET)!;
		expect(stored.refreshToken).toBe('refresh-2');
		expect(e.sets[0]!.value).not.toContain('refresh-2');
		expect(isfc.to('/oauth2/token')[0]!.headers.get('user-agent')).toBe('UA/1');
	});

	it('does nothing while the token has time left (no call to ISFC)', async () => {
		const isfc = createFakeIsfc();
		const e = eventFor(isfcSession({ accessExp: Math.floor(Date.now() / 1000) + 600 }), isfc.fetch);
		expect(await refreshForEvent(e.event as never, { fetch: isfc.fetch })).toBe('fresh');
		expect(isfc.calls).toHaveLength(0);
	});

	it('force refreshes even with time left (the gateway refused the token)', async () => {
		const isfc = createFakeIsfc();
		const e = eventFor(isfcSession({ accessExp: Math.floor(Date.now() / 1000) + 600 }), isfc.fetch);
		expect(await refreshForEvent(e.event as never, { fetch: isfc.fetch, force: true })).toBe(
			'refreshed'
		);
	});

	it('when ISFC refuses, the session ends: cookie cleared, flagged, and the app redirects to /login?expired=1', async () => {
		const isfc = createFakeIsfc({ token: () => isfcError(401, 'AS_FB_01') });
		const e = eventFor(isfcSession({ accessExp: 1, refreshToken: 'dead' }), isfc.fetch);
		expect(await refreshForEvent(e.event as never, { fetch: isfc.fetch })).toBe('ended');
		expect(e.event.locals.session).toBeNull();
		expect(e.event.locals.sessionEnded).toBe(true);
		expect(e.deleted).toContain('hub_console_session');
		const err = await Promise.resolve(
			appLayoutLoad({
				locals: e.event.locals,
				url: new URL('https://console.test/tenants')
			} as never)
		).catch((x) => x);
		expect(err).toMatchObject({ status: 303, location: '/login?next=%2Ftenants&expired=1' });
	});

	it('when ISFC is down the session is kept, so the operator is not signed out by an outage', async () => {
		const isfc = createFakeIsfc({ token: () => new Response('x', { status: 503 }) });
		const s = isfcSession({ accessExp: 1, refreshToken: 'live' });
		const e = eventFor(s, isfc.fetch);
		expect(await refreshForEvent(e.event as never, { fetch: isfc.fetch })).toBe('failed');
		expect(e.event.locals.session).toBe(s);
		expect(e.deleted).toHaveLength(0);
	});

	it('authenticate runs the refresh before the request uses the token', async () => {
		const isfc = createFakeIsfc({ token: () => tokenResponse({ refresh_token: 'refresh-3' }) });
		const near = isfcSession({ accessExp: Math.floor(Date.now() / 1000) + 5 });
		const { encodeSession } = await import('./session');
		const e = eventFor(null, isfc.fetch, encodeSession(near, SECRET));
		await authenticate({ event: e.event as never, resolve: async () => new Response('ok') });
		expect(e.event.locals.session).toMatchObject({ refreshToken: 'refresh-3' });
		expect(isfc.to('/oauth2/token')).toHaveLength(1);
	});

	it('an expired console session (cookie past its lifetime) is flagged so the login page says so', async () => {
		const { encodeSession } = await import('./session');
		const old = { ...isfcSession(), exp: 1 };
		const e = eventFor(null, createFakeIsfc().fetch, encodeSession(old, SECRET));
		await authenticate({ event: e.event as never, resolve: async () => new Response('ok') });
		expect(e.event.locals.session).toBeNull();
		expect(e.event.locals.sessionEnded).toBe(true);
	});
});

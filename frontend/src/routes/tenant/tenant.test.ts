import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
	AUTH,
	GATEWAY,
	ORDERS_TENANT,
	PLATFORM_TENANT,
	createFakeIsfc
} from '$lib/testing/fake-isfc';
import { decodeSession, newSession, type Session } from '$lib/server/auth/session';
import { readSessionCookie } from '$lib/server/auth/cookie';
import { POST } from './+server';

// HUB-494: the tenant switch is kept in the session, and checked against ISFC.
const SECRET = 's'.repeat(40);
const env = vi.hoisted(() => ({
	priv: {} as Record<string, string | undefined>,
	pub: {} as Record<string, string | undefined>
}));
vi.mock('$env/dynamic/private', () => ({ env: env.priv }));
vi.mock('$env/dynamic/public', () => ({ env: env.pub }));

beforeEach(() => {
	for (const k of Object.keys(env.priv)) delete env.priv[k];
	Object.assign(env.priv, {
		HUB_ISFC_AUTH_URL: AUTH,
		HUB_ISFC_GATEWAY_URL: GATEWAY,
		HUB_SESSION_SECRET: SECRET
	});
	env.pub.PUBLIC_AUTH_MODE = 'isfc';
});

const session = (): Session =>
	newSession({
		provider: 'isfc',
		user: { subject: 'u', name: null, email: null, roles: [], tenants: [] },
		accessToken: 'access-1',
		refreshToken: 'refresh-1',
		isfcSessionId: 'sess-1',
		accessExp: Math.floor(Date.now() / 1000) + 600,
		deviceId: 'device-00000000-0000-4000-8000-000000000001',
		tenantId: PLATFORM_TENANT
	});

function post(
	fields: Record<string, string>,
	opts: { json?: boolean; session?: Session | null; fetch?: typeof fetch } = {}
) {
	const body = new URLSearchParams(fields);
	const store = new Map<string, string>();
	const cookies = {
		get: (k: string) => store.get(k),
		set: (k: string, v: string) => void store.set(k, v),
		delete: (k: string) => void store.delete(k)
	};
	const locals = {
		session: opts.session === undefined ? session() : opts.session,
		sessionEnded: false
	};
	const result = POST({
		request: new Request('https://console.test/tenant', {
			method: 'POST',
			body,
			headers: opts.json ? { accept: 'application/json' } : undefined
		}),
		locals,
		cookies,
		url: new URL('https://console.test/tenant'),
		fetch: opts.fetch ?? createFakeIsfc().fetch
	} as never);
	return { result, cookies, locals };
}

describe('POST /tenant', () => {
	it('keeps a tenant ISFC lists for the user in the session (JSON)', async () => {
		const { result, cookies, locals } = post(
			{ tenant_id: ORDERS_TENANT.toUpperCase() },
			{ json: true }
		);
		const res = (await result) as Response;
		expect(await res.json()).toEqual({ ok: true, tenantId: ORDERS_TENANT });
		expect(decodeSession(readSessionCookie(cookies), SECRET)).toMatchObject({
			tenantId: ORDERS_TENANT,
			accessToken: 'access-1'
		});
		expect(locals.session?.tenantId).toBe(ORDERS_TENANT);
	});

	it('a plain form post answers 303 back to the page it came from', async () => {
		const { result } = post({ tenant_id: ORDERS_TENANT, next: '/vendors?q=a' });
		await expect(result).rejects.toMatchObject({ status: 303, location: '/vendors?q=a' });
	});

	it('never goes off-site after the switch', async () => {
		const { result } = post({ tenant_id: ORDERS_TENANT, next: '//evil.example' });
		await expect(result).rejects.toMatchObject({ location: '/' });
	});

	it('refuses a tenant that is not in the user list, and changes nothing', async () => {
		const { result, cookies, locals } = post(
			{ tenant_id: '99999999-9999-4999-8999-999999999999' },
			{ json: true }
		);
		const res = (await result) as Response;
		expect(res.status).toBe(403);
		expect((await res.json()).message).toMatch(/no access to that tenant/);
		expect(readSessionCookie(cookies)).toBeUndefined();
		expect(locals.session?.tenantId).toBe(PLATFORM_TENANT);
	});

	it.each(['', 'not-a-uuid', "'; drop table"])('refuses %j', async (tenant_id) => {
		const { result } = post({ tenant_id }, { json: true });
		expect(((await result) as Response).status).toBe(403);
	});

	it('answers 503 when ISFC cannot list the tenants', async () => {
		const down = createFakeIsfc({ panels: () => new Response('x', { status: 503 }) });
		const { result, cookies } = post(
			{ tenant_id: ORDERS_TENANT },
			{ json: true, fetch: down.fetch }
		);
		expect(((await result) as Response).status).toBe(503);
		expect(readSessionCookie(cookies)).toBeUndefined();
	});

	it('needs an ISFC session', async () => {
		const none = post({ tenant_id: ORDERS_TENANT }, { json: true, session: null });
		expect(((await none.result) as Response).status).toBe(401);
		const noJson = post({ tenant_id: ORDERS_TENANT }, { session: null });
		await expect(noJson.result).rejects.toMatchObject({ status: 303, location: '/login' });
		env.pub.PUBLIC_AUTH_MODE = 'dev';
		const dev = post({ tenant_id: ORDERS_TENANT }, { json: true });
		expect(((await dev.result) as Response).status).toBe(401);
	});
});

import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
	AUTH,
	BILLING_TENANT,
	GATEWAY,
	ORDERS_TENANT,
	PLATFORM_TENANT,
	createFakeIsfc,
	isfcError,
	panelsResponse,
	tokenResponse
} from '$lib/testing/fake-isfc';
import type { IsfcConfig } from './config';
import { fetchSwitchTargets, loadSwitchTargets, tenantChoices } from './panels';
import { resetRefreshState } from './refresh';
import { newSession } from './session';

// HUB-494: the tenant list from GET {gateway}/auth/oauth2/panels, platform tenant first.
const SECRET = 's'.repeat(40);
const env = vi.hoisted(() => ({ priv: {} as Record<string, string | undefined> }));
vi.mock('$env/dynamic/private', () => ({ env: env.priv }));
vi.mock('$env/dynamic/public', () => ({ env: { PUBLIC_AUTH_MODE: 'isfc' } }));

const config: IsfcConfig = {
	authUrl: AUTH,
	clientId: 'integration-hub-console',
	redirectUri: null,
	gatewayUrl: GATEWAY,
	platformTenantId: PLATFORM_TENANT
};

beforeEach(() => {
	resetRefreshState();
	for (const k of Object.keys(env.priv)) delete env.priv[k];
	Object.assign(env.priv, {
		HUB_ISFC_AUTH_URL: AUTH,
		HUB_ISFC_GATEWAY_URL: GATEWAY,
		HUB_ISFC_PLATFORM_TENANT_ID: PLATFORM_TENANT,
		HUB_SESSION_SECRET: SECRET
	});
});

describe('tenantChoices', () => {
	it('lists the console panel tenants with the platform tenant first, others in ISFC order', async () => {
		const body = await panelsResponse().json();
		expect(tenantChoices(body, config).map((c) => [c.key, c.platform])).toEqual([
			['INTEGRATION_HUB_PLATFORM', true],
			['ORDERS', false],
			['BILLING', false]
		]);
	});

	it('keeps ISFC order when no platform tenant is configured', async () => {
		const body = await panelsResponse().json();
		expect(tenantChoices(body, { ...config, platformTenantId: null }).map((c) => c.key)).toEqual([
			'ORDERS',
			'INTEGRATION_HUB_PLATFORM',
			'BILLING'
		]);
	});

	it('ignores other panels, bad ids and duplicates; falls back to key and id for names', () => {
		const body = {
			data: {
				panels: [
					{
						client_id: 'other',
						current: false,
						tenants: [{ id: BILLING_TENANT, key: 'X', name: 'X' }]
					},
					{
						client_id: 'integration-hub-console',
						current: true,
						tenants: [
							{ id: 'not-a-uuid', key: 'BAD' },
							{ id: ORDERS_TENANT.toUpperCase(), key: 'ORDERS' },
							{ id: ORDERS_TENANT, key: 'DUP', name: 'Dup' },
							{ id: BILLING_TENANT }
						]
					}
				]
			}
		};
		expect(tenantChoices(body, config)).toEqual([
			{ id: ORDERS_TENANT, key: 'ORDERS', name: 'ORDERS', platform: false },
			{ id: BILLING_TENANT, key: BILLING_TENANT, name: BILLING_TENANT, platform: false }
		]);
	});

	it('is empty for a panel without tenants, null data or no panels', () => {
		expect(tenantChoices({ data: null }, config)).toEqual([]);
		expect(tenantChoices({ data: { panels: [] } }, config)).toEqual([]);
		expect(tenantChoices({ data: { panels: [{ current: true, tenants: null }] } }, config)).toEqual(
			[]
		);
	});
});

describe('fetchSwitchTargets', () => {
	it('calls the gateway panels endpoint with the access token and the device id', async () => {
		const isfc = createFakeIsfc();
		const out = await fetchSwitchTargets(config, isfc.fetch, {
			accessToken: 'access-1',
			deviceId: 'device-x',
			userAgent: 'UA/1'
		});
		expect(out).toHaveLength(3);
		const call = isfc.to('/auth/oauth2/panels')[0]!;
		expect(call.method).toBe('GET');
		expect(call.url.origin).toBe(GATEWAY);
		expect(call.headers.get('authorization')).toBe('Bearer access-1');
		expect(call.headers.get('x-device-id')).toBe('device-x');
	});

	it('needs the gateway URL', async () => {
		await expect(
			fetchSwitchTargets({ ...config, gatewayUrl: null }, createFakeIsfc().fetch, {
				accessToken: 'a',
				deviceId: null,
				userAgent: null
			})
		).rejects.toMatchObject({ code: 'not_configured' });
	});
});

function event(fetchImpl: typeof fetch, over: Partial<ReturnType<typeof newSession>> = {}) {
	const session = newSession({
		provider: 'isfc',
		user: { subject: 'u', name: null, email: null, roles: [], tenants: [] },
		accessToken: 'access-1',
		refreshToken: 'refresh-1',
		isfcSessionId: 'sess-1',
		accessExp: Math.floor(Date.now() / 1000) + 600,
		deviceId: 'device-00000000-0000-4000-8000-000000000001',
		...over
	});
	const cookies = { get: () => undefined, set: vi.fn(), delete: vi.fn() };
	return {
		locals: { session, sessionEnded: false },
		cookies,
		request: new Request('https://console.test/', { headers: { 'user-agent': 'UA/1' } }),
		url: new URL('https://console.test/'),
		fetch: fetchImpl
	};
}

describe('loadSwitchTargets', () => {
	it('returns the choices', async () => {
		const isfc = createFakeIsfc();
		const r = await loadSwitchTargets(event(isfc.fetch) as never);
		expect(r).toMatchObject({ ok: true });
		expect(r.ok && r.choices[0]!.platform).toBe(true);
	});

	it('refreshes once when the gateway refuses the token, then tries again', async () => {
		let panelCalls = 0;
		const isfc = createFakeIsfc({
			panels: () => (++panelCalls === 1 ? isfcError(401, 'GW_004') : panelsResponse()),
			token: () => tokenResponse({ refresh_token: 'refresh-2' })
		});
		const e = event(isfc.fetch);
		const r = await loadSwitchTargets(e as never);
		expect(r.ok).toBe(true);
		expect(isfc.to('/oauth2/token')).toHaveLength(1);
		expect(isfc.to('/auth/oauth2/panels')).toHaveLength(2);
		expect(isfc.to('/auth/oauth2/panels')[1]!.headers.get('authorization')).not.toBe(
			'Bearer access-1'
		);
	});

	it('says so, and does not throw, when ISFC fails or the gateway is not configured', async () => {
		const down = createFakeIsfc({ panels: () => new Response('x', { status: 503 }) });
		expect(await loadSwitchTargets(event(down.fetch) as never)).toMatchObject({
			ok: false,
			message: expect.stringMatching(/did not return your tenants/)
		});
		delete env.priv.HUB_ISFC_GATEWAY_URL;
		expect(await loadSwitchTargets(event(down.fetch) as never)).toMatchObject({
			ok: false,
			message: expect.stringMatching(/HUB_ISFC_GATEWAY_URL/)
		});
	});
});

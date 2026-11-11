import { render, screen } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { HubApiError } from '$lib/api/errors';
import AccessUnavailable from '$lib/components/AccessUnavailable.svelte';
import {
	AUTH,
	GATEWAY,
	ORDERS_TENANT,
	PLATFORM_TENANT,
	createFakeIsfc
} from '$lib/testing/fake-isfc';
import { newSession } from '$lib/server/auth/session';
import { load } from './+layout.server';

// HUB-494: the layout gives the switcher its tenants; GW_015 has a readable message.
const env = vi.hoisted(() => ({
	priv: {} as Record<string, string | undefined>,
	pub: {} as Record<string, string | undefined>
}));
vi.mock('$env/dynamic/private', () => ({ env: env.priv }));
vi.mock('$env/dynamic/public', () => ({ env: env.pub }));
vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn() }));

beforeEach(() => {
	for (const k of Object.keys(env.priv)) delete env.priv[k];
	Object.assign(env.priv, {
		HUB_ISFC_AUTH_URL: AUTH,
		HUB_ISFC_GATEWAY_URL: GATEWAY,
		HUB_ISFC_PLATFORM_TENANT_ID: PLATFORM_TENANT,
		HUB_SESSION_SECRET: 's'.repeat(40)
	});
	env.pub.PUBLIC_AUTH_MODE = 'isfc';
});

const isfcSession = (tenantId: string | null) =>
	newSession({
		provider: 'isfc',
		user: { subject: 'u', name: null, email: null, roles: [], tenants: [] },
		accessToken: 'access-secret',
		refreshToken: 'refresh-secret',
		isfcSessionId: 's',
		accessExp: Math.floor(Date.now() / 1000) + 600,
		deviceId: 'device-00000000-0000-4000-8000-000000000001',
		tenantId
	});

function run(session: ReturnType<typeof isfcSession>, fetchImpl: typeof fetch) {
	return load({
		locals: { session, sessionEnded: false },
		url: new URL('https://console.test/vendors'),
		cookies: { get: () => undefined, set: () => {}, delete: () => {} },
		request: new Request('https://console.test/vendors'),
		fetch: fetchImpl
	} as never) as Promise<{
		tenantSwitcher: { choices: { id: string }[]; currentId: string | null; error: string | null };
		isfcMode: boolean;
	}>;
}

describe('(app) layout and the tenant switcher', () => {
	it('lists the tenants with the platform tenant first; the session choice is current', async () => {
		const out = await run(isfcSession(ORDERS_TENANT), createFakeIsfc().fetch);
		expect(out.isfcMode).toBe(true);
		expect(out.tenantSwitcher.choices[0]!.id).toBe(PLATFORM_TENANT);
		expect(out.tenantSwitcher.currentId).toBe(ORDERS_TENANT);
		expect(out.tenantSwitcher.error).toBeNull();
	});

	it('without a choice the current tenant is ISFC default: the first (platform) one', async () => {
		const out = await run(isfcSession(null), createFakeIsfc().fetch);
		expect(out.tenantSwitcher.currentId).toBe(PLATFORM_TENANT);
	});

	it('a stale choice (no longer allowed) falls back to the first tenant', async () => {
		const out = await run(
			isfcSession('99999999-9999-4999-8999-999999999999'),
			createFakeIsfc().fetch
		);
		expect(out.tenantSwitcher.currentId).toBe(PLATFORM_TENANT);
	});

	it('the page never gets a token', async () => {
		const out = await run(isfcSession(null), createFakeIsfc().fetch);
		const json = JSON.stringify(out);
		expect(json).not.toContain('access-secret');
		expect(json).not.toContain('refresh-secret');
	});

	it('the layout still loads when ISFC cannot list the tenants', async () => {
		const down = createFakeIsfc({ panels: () => new Response('x', { status: 503 }) });
		const out = await run(isfcSession(null), down.fetch);
		expect(out.tenantSwitcher).toMatchObject({ choices: [], currentId: null });
		expect(out.tenantSwitcher.error).toMatch(/did not return your tenants/);
	});
});

describe('GW_015 (no access to the tenant)', () => {
	it('is shown with its message and a pointer to the switcher', () => {
		const error = new HubApiError({
			status: 403,
			code: 'FORBIDDEN',
			message:
				'You have no access to that tenant in the Integration Hub. Pick another tenant, or ask for access in ISFC.',
			meta: { isfc_error_code: 'GW_015' }
		});
		render(AccessUnavailable, { props: { error } });
		expect(screen.getByText(/no access to that tenant in the Integration Hub/)).toBeInTheDocument();
		expect(screen.getByRole('note')).toHaveTextContent('tenant switcher');
	});

	it('other errors do not mention the switcher', () => {
		const error = new HubApiError({ status: 500, code: 'INTERNAL_ERROR', message: 'boom' });
		render(AccessUnavailable, { props: { error } });
		expect(screen.queryByText(/tenant switcher/)).toBeNull();
	});
});

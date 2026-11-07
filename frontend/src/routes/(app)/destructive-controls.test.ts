import { render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockOperation } from '$lib/api/mocks/handlers';
import { presets } from '$lib/api/mocks/scenarios';
import { server } from '$lib/api/mocks/server';
import { requiredRoles, type OperationId } from '$lib/api/roles.gen';
import type { Breaker, Role } from '$lib/api/types';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import { useActions } from '$lib/testing/forms';
import DlqPage from './dlq/+page.svelte';
import { load as dlqLoad } from './dlq/+page';
import HealthPage from './health/+page.svelte';
import { load as healthLoad } from './health/+page';
import IntegrationPage from './integrations/[integration_id]/+page.svelte';
import { load as integrationLoad } from './integrations/[integration_id]/+page';
import TenantsPage from './tenants/+page.svelte';
import { load as tenantsLoad } from './tenants/+page';
import TenantPage from './tenants/[tenant_id]/+page.svelte';
import { load as tenantLoad } from './tenants/[tenant_id]/+page';
import { actions as tenantActions } from './tenants/[tenant_id]/+page.server';
import TransformerVersionPage from './transformers/[transformer_id]/versions/[version]/+page.svelte';
import { load as transformerVersionLoad } from './transformers/[transformer_id]/versions/[version]/+page';
import { actions as transformerVersionActions } from './transformers/[transformer_id]/versions/[version]/+page.server';

// HUB-288: "If a user doesn't have access there should be no delete option at all." Every
// destructive or mutating control is rendered only for roles that satisfy its operation's
// `x-required-role` (contract), and is absent (not disabled) otherwise. One matrix per screen,
// every role. `min` is written out here on purpose and checked against the generated table, so a
// contract change that moves a role makes this test fail until someone looks at the screens.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), invalidate: vi.fn(), goto: vi.fn() }));

const ROLES: Role[] = ['admin', 'editor', 'viewer', 'tenant_viewer'];
const RANK: Record<Role, number> = { tenant_viewer: 0, viewer: 1, editor: 2, admin: 3 };
const TENANT = 'orders-service';

interface Control {
	/** Accessible name of the control (button or link). */
	name: RegExp;
	op: OperationId;
	min: Role;
	kind?: 'button' | 'link';
}

interface Screen {
	screen: string;
	/** Renders the screen for `access` and resolves when its data is on screen. */
	mount: (access: Access) => Promise<void>;
	controls: Control[];
}

const access = (role: Role) => accessFromRoles([role], role === 'tenant_viewer' ? [TENANT] : []);

const breaker = contractMocks.listBreakers.example.items[0]!;
const b = (over: Partial<Breaker>): Breaker => ({ ...breaker, ...over });

const screens: Screen[] = [
	{
		screen: 'DLQ',
		async mount(a) {
			const data = dlqLoad({ fetch, url: new URL('http://console.test/dlq') } as never) as object;
			render(DlqPage, { props: { data: { access: a, ...data } as never } });
			await screen.findByRole('table');
		},
		controls: [
			{ name: /^Replay matching/, op: 'replayDeadLetters', min: 'editor' },
			{ name: /^Purge matching/, op: 'purgeDeadLetters', min: 'admin' }
		]
	},
	{
		screen: 'Health',
		async mount(a) {
			server.use(
				mockOperation('listBreakers', {
					items: [
						b({ account_id: 'ct-brandA-in', forced: 'open', state: 'open' }),
						b({ account_id: 'ct-brandB-us', forced: 'none', state: 'closed' })
					],
					next_cursor: null
				})
			);
			const data = healthLoad({
				fetch,
				url: new URL('http://console.test/health'),
				depends: vi.fn()
			} as never) as object;
			render(HealthPage, { props: { data: { access: a, ...data } as never } });
			// Tenant viewers get a note instead of the board (vendors and accounts are hidden).
			await (a.tenantScoped ? screen.findByRole('note') : screen.findByRole('table'));
		},
		controls: [
			{ name: /^Force open$/, op: 'forceOpenBreaker', min: 'admin' },
			{ name: /^Force closed$/, op: 'forceCloseBreaker', min: 'admin' },
			{ name: /^Clear force$/, op: 'clearBreakerForce', min: 'admin' }
		]
	},
	{
		screen: 'Integration detail',
		async mount(a) {
			// v8 active and v7 published, so a rollback target exists.
			server.use(...presets['v8-active']!());
			const data = integrationLoad({
				fetch,
				url: new URL('http://console.test/integrations/int_evt_brandA'),
				params: { integration_id: 'int_evt_brandA' }
			} as never) as object;
			render(IntegrationPage, { props: { data: { access: a, ...data } as never } });
			await screen.findByRole('table', { name: /Versions of/, busy: false });
			await screen.findByText('Owner tenant');
		},
		controls: [
			{ name: /(Pause|Resume) integration/, op: 'pauseIntegration', min: 'editor' },
			{ name: /^Roll back to v\d+$/, op: 'rollbackIntegration', min: 'editor' },
			{
				name: /^(Edit a new draft|Review draft v\d+ and publish)$/,
				op: 'createIntegrationDraft',
				min: 'editor',
				kind: 'link'
			}
		]
	},
	{
		screen: 'Tenants',
		async mount(a) {
			const data = await tenantsLoad({
				fetch,
				url: new URL('http://console.test/tenants'),
				parent: async () => ({ access: a })
			} as never);
			render(TenantsPage, { props: { data: { access: a, ...(data as object) } as never } });
			await screen.findByRole('table');
		},
		controls: [{ name: /^Create tenant$/, op: 'createTenant', min: 'admin' }]
	},
	{
		screen: 'Tenant detail',
		async mount(a) {
			useActions(tenantActions, { params: { tenant_id: TENANT } });
			const data = await tenantLoad({
				fetch,
				url: new URL(`http://console.test/tenants/${TENANT}`),
				params: { tenant_id: TENANT },
				parent: async () => ({ access: a })
			} as never);
			render(TenantPage, { props: { data: { access: a, ...(data as object) } as never } });
			await screen.findByRole('region', { name: 'Summary' });
			await screen.findByRole('table', { name: `API keys of ${TENANT}` });
			await screen.findByRole('table', { name: `Callback endpoints of ${TENANT}` });
		},
		controls: [
			{ name: /^Edit tenant$/, op: 'updateTenant', min: 'admin' },
			{ name: /^(Disable|Enable) tenant$/, op: 'updateTenant', min: 'admin' },
			{ name: /^Create API key$/, op: 'createApiKey', min: 'admin' },
			{ name: /^Rotate hk_/, op: 'rotateApiKey', min: 'admin' },
			{ name: /^Revoke hk_/, op: 'revokeApiKey', min: 'admin' },
			{ name: /^Register endpoint$/, op: 'createRegistrarEndpoint', min: 'admin' },
			{ name: /^Edit reg_/, op: 'updateRegistrarEndpoint', min: 'admin' },
			{ name: /^(Disable|Enable) reg_/, op: 'updateRegistrarEndpoint', min: 'admin' },
			{ name: /^Rotate secret of reg_/, op: 'rotateRegistrarSecret', min: 'admin' },
			{ name: /^Delete reg_/, op: 'deleteRegistrarEndpoint', min: 'admin' }
		]
	},
	{
		screen: 'Transformer version',
		async mount(a) {
			useActions(transformerVersionActions, { params: { transformer_id: 'T1', version: '7' } });
			const data = transformerVersionLoad({
				fetch,
				url: new URL('http://console.test/transformers/T1/versions/7'),
				params: { transformer_id: 'T1', version: '7' }
			} as never) as object;
			render(TransformerVersionPage, { props: { data: { access: a, ...data } as never } });
			await (a.tenantScoped
				? screen.findByRole('note')
				: screen.findByLabelText('T1@7 source (YAML)'));
		},
		controls: [
			{ name: /^Run samples for T1@7$/, op: 'runTransformerGoldens', min: 'editor' },
			{ name: /^Dry-run T1@7$/, op: 'dryRunTransformer', min: 'editor' }
		]
	}
];

describe('destructive controls follow x-required-role (HUB-288)', () => {
	it('the matrix agrees with the contract', () => {
		for (const s of screens) {
			for (const c of s.controls)
				expect(`${c.op}: ${requiredRoles[c.op]}`).toBe(`${c.op}: ${c.min}`);
		}
	});

	describe.each(screens)('$screen', (s) => {
		it.each(ROLES)('%s sees exactly the controls its role allows', async (role) => {
			await s.mount(access(role));
			for (const c of s.controls) {
				const found = screen.queryAllByRole(c.kind ?? 'button', { name: c.name });
				const allowed = RANK[role] >= RANK[c.min];
				expect({ control: String(c.name), present: found.length > 0 }).toEqual({
					control: String(c.name),
					present: allowed
				});
				// Never rendered disabled instead of hidden.
				for (const el of found) expect(el).not.toHaveAttribute('aria-disabled', 'true');
			}
		});
	});

	it('nothing gated renders when /me failed (fails closed)', async () => {
		const { NO_ACCESS } = await import('$lib/auth/roles');
		await screens[0]!.mount(NO_ACCESS);
		expect(screen.queryByRole('button', { name: /^Replay matching/ })).toBeNull();
		expect(screen.queryByRole('button', { name: /^Purge matching/ })).toBeNull();
	});
});

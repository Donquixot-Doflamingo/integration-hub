import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import type { Role } from '$lib/api/types';
import { accessFromRoles } from '$lib/auth/roles';
import { takeFlash } from '$lib/stores/flash.svelte';
import { useActions } from '$lib/testing/forms';
import Page from './+page.svelte';
import { load } from './+page';
import { actions } from './+page.server';
import { createBody, emptyValues, grantList, problems } from './integration-form';
import ListPage from '../+page.svelte';
import { load as listLoad } from '../+page';

// HUB-292: create an integration (createIntegration, editor) and open its first draft.

const goto = vi.fn();
vi.mock('$app/navigation', () => ({
	goto: (...a: unknown[]) => goto(...a),
	invalidateAll: vi.fn()
}));

const REASON = 'Brand C events for the growth team';

async function renderNew(role: Role = 'editor') {
	const access = accessFromRoles([role]);
	const data = load({ fetch, url: new URL('http://console.test/integrations/new') } as never);
	render(Page, { props: { data: { access, ...(data as object) } as never } });
}

beforeEach(() => {
	goto.mockReset();
	takeFlash();
	useActions(actions);
});

describe('integration form', () => {
	it('checks the id, owner, capability and grants', () => {
		const v = { ...emptyValues(), integration_id: 'x', capability: 'events' };
		expect(problems(v)).toMatchObject({
			'/integration_id': expect.stringMatching(/3 to 64/),
			'/owner_tenant': expect.stringMatching(/Pick the tenant/),
			'/capability': expect.stringMatching(/events.track@v1/)
		});
		expect(
			problems({
				...v,
				integration_id: 'int_c',
				owner_tenant: 'orders-service',
				capability: 'events.track@v1',
				grants: 'orders-service'
			})['/grants']
		).toMatch(/owner can always call it/);
		expect(grantList('a-svc, b-svc  a-svc')).toEqual(['a-svc', 'b-svc']);
	});

	it('builds IntegrationCreate', () => {
		const r = createBody(
			{
				integration_id: 'int_evt_brandC',
				owner_tenant: 'orders-service',
				capability: 'events.track@v1',
				description: ' Brand C ',
				grants: 'billing-service',
				moves_money_or_quota: false
			},
			REASON
		);
		expect(r).toEqual({
			ok: true,
			body: {
				integration_id: 'int_evt_brandC',
				owner_tenant: 'orders-service',
				capability: 'events.track@v1',
				description: 'Brand C',
				grants: ['billing-service'],
				moves_money_or_quota: false,
				reason: REASON
			}
		});
	});
});

describe('create integration page', () => {
	it('creates it and opens the first draft with the change log id', async () => {
		const bodies: unknown[] = [];
		server.use(
			http.post(mswPath('/integrations'), async ({ request }) => {
				bodies.push(await request.json());
				return HttpResponse.json(contractMocks.createIntegration.example, { status: 201 });
			})
		);
		await renderNew();
		await userEvent.type(await screen.findByLabelText(/^Integration id/), 'int_evt_brandC');
		await userEvent.selectOptions(screen.getByLabelText(/^Owner tenant/), 'orders-service');
		await userEvent.selectOptions(screen.getByLabelText(/^Capability/), 'events.track@v1');
		await userEvent.click(screen.getByRole('checkbox', { name: /Moves money/ }));
		await userEvent.type(screen.getByLabelText(/^Reason/), REASON);
		await userEvent.click(screen.getByRole('button', { name: 'Create integration' }));
		const id = contractMocks.createIntegration.example.integration.integration_id;
		await waitFor(() => expect(goto).toHaveBeenCalledWith(`/integrations/${id}/publish`));
		expect(bodies[0]).toMatchObject({
			integration_id: 'int_evt_brandC',
			owner_tenant: 'orders-service',
			capability: 'events.track@v1',
			moves_money_or_quota: true,
			reason: REASON
		});
		expect(takeFlash()?.changeId).toBe(contractMocks.createIntegration.example.change_id);
	});

	it('a taken id shows the API message on the page', async () => {
		server.use(
			mockError('createIntegration', 409, {
				message: 'Conflict: integration int_evt_brandC exists'
			})
		);
		await renderNew();
		await userEvent.type(await screen.findByLabelText(/^Integration id/), 'int_evt_brandC');
		await userEvent.selectOptions(screen.getByLabelText(/^Owner tenant/), 'orders-service');
		await userEvent.selectOptions(screen.getByLabelText(/^Capability/), 'events.track@v1');
		await userEvent.type(screen.getByLabelText(/^Reason/), REASON);
		await userEvent.click(screen.getByRole('button', { name: 'Create integration' }));
		expect(await screen.findByRole('alert')).toHaveTextContent('integration int_evt_brandC exists');
		expect(goto).not.toHaveBeenCalled();
	});

	it.each<Role>(['viewer', 'tenant_viewer'])(
		'%s gets a read-only note and no create button',
		async (role) => {
			await renderNew(role);
			expect(screen.getByRole('note')).toHaveTextContent(
				'Creating integrations needs the Editor role'
			);
			const access = accessFromRoles([role], role === 'tenant_viewer' ? ['orders-service'] : []);
			const data = listLoad({
				fetch,
				url: new URL('http://console.test/integrations'),
				parent: async () => ({ access })
			} as never);
			render(ListPage, { props: { data: { access, ...(data as object) } as never } });
			await screen.findByRole('table', { busy: false });
			expect(screen.queryByRole('link', { name: 'Create integration' })).toBeNull();
		}
	);

	it('editors get "Create integration" on the list', async () => {
		const access = accessFromRoles(['editor']);
		const data = listLoad({ fetch, url: new URL('http://console.test/integrations') } as never);
		render(ListPage, { props: { data: { access, ...(data as object) } as never } });
		expect(screen.getByRole('link', { name: 'Create integration' })).toHaveAttribute(
			'href',
			'/integrations/new'
		);
	});
});

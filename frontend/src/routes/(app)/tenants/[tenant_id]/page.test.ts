import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import type { Role } from '$lib/api/types';
import { runAction, useActions } from '$lib/testing/forms';
import Page from './+page.svelte';
import { load } from './+page';
import { actions } from './+page.server';

// HUB-270: tenant detail, edit (merge-patch + If-Match) and disable / enable, admin only.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const ID = 'orders-service';
const REASON = 'Festive season traffic, OPS-142';
const tenant = contractMocks.getTenant.example;

async function renderDetail(access: Access = accessFromRoles(['admin']), id = ID) {
	const data = await load({
		fetch,
		url: new URL(`http://console.test/tenants/${id}`),
		params: { tenant_id: id },
		parent: async () => ({ access })
	} as never);
	render(Page, { props: { data: { access, ...(data as object) } as never } });
	return screen.findByRole('region', { name: 'Summary' });
}

/** Captures PATCH /tenants/{id}: body and If-Match. */
function capturePatch(answer: object = contractMocks.updateTenant.example) {
	const seen: { body: unknown; ifMatch: string | null }[] = [];
	server.use(
		http.patch(mswPath('/tenants/:tenant_id'), async ({ request }) => {
			seen.push({ body: await request.json(), ifMatch: request.headers.get('If-Match') });
			return HttpResponse.json(answer);
		})
	);
	return seen;
}

beforeEach(() => {
	useActions(actions, { params: { tenant_id: ID } });
});

describe('tenant detail', () => {
	it('shows the contract fields and links the owned integrations', async () => {
		const head = await renderDetail();
		expect(head).toHaveTextContent('Active');
		expect(head).toHaveTextContent('Orders service');
		expect(head).toHaveTextContent('orders-team@example.com');
		expect(head).toHaveTextContent('200 requests/s, burst 400');
		expect(within(head).getByRole('link', { name: '3' })).toHaveAttribute(
			'href',
			'/integrations?tenant_id=orders-service'
		);
		expect(head).toHaveTextContent('grants) is set on each integration');
	});

	it.each<Role>(['editor', 'viewer', 'tenant_viewer'])('%s gets a read-only note', async (role) => {
		const head = await renderDetail(accessFromRoles([role], role === 'tenant_viewer' ? [ID] : []));
		expect(within(head).getByRole('note')).toHaveTextContent('needs the Admin role');
		expect(within(head).queryByRole('button')).toBeNull();
	});

	it('a tenant viewer asking for another tenant gets 404 without a call', async () => {
		const calls: string[] = [];
		server.events.on('request:start', ({ request }) => calls.push(request.url));
		await expect(
			load({
				fetch,
				url: new URL('http://console.test/tenants/billing'),
				params: { tenant_id: 'billing' },
				parent: async () => ({ access: accessFromRoles(['tenant_viewer'], [ID]) })
			} as never)
		).rejects.toMatchObject({ status: 404, body: { code: 'NOT_FOUND' } });
		expect(calls).toEqual([]);
		server.events.removeAllListeners();
	});
});

describe('edit tenant', () => {
	async function openEdit() {
		const user = userEvent.setup();
		const head = await renderDetail();
		await user.click(within(head).getByRole('button', { name: 'Edit tenant' }));
		return { user, dialog: await screen.findByRole('dialog', { name: `Edit tenant ${ID}` }) };
	}

	it('opens with the current values, sends only the change with If-Match, shows the change id', async () => {
		const seen = capturePatch();
		const { user, dialog } = await openEdit();
		const rps = within(dialog).getByLabelText('Requests per second');
		expect(rps).toHaveValue('200');
		expect(within(dialog).getByLabelText(/Display name/)).toHaveValue('Orders service');
		const save = within(dialog).getByRole('button', { name: 'Save changes' });
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		expect(save).toBeDisabled();
		expect(dialog).toHaveTextContent('Change at least one field to save.');
		await user.clear(rps);
		await user.type(rps, '300');
		await user.clear(within(dialog).getByLabelText('Burst'));
		await user.type(within(dialog).getByLabelText('Burst'), '600');
		await user.click(save);
		const notice = await screen.findByText(/Saved orders-service/);
		expect(notice).toHaveTextContent('300 requests/s, burst 600');
		expect(notice.closest('[role="status"]')).toHaveTextContent('chg_01J9Z4B2C3D4E5F6G7H8J9K0NB');
		expect(seen).toEqual([
			{ body: { rate_limit: { rps: 300, burst: 600 }, reason: REASON }, ifMatch: '"3"' }
		]);
	});

	it('412: someone else changed the tenant, offers Reload and keeps the reason', async () => {
		server.use(mockError('updateTenant', 412));
		const { user, dialog } = await openEdit();
		await user.clear(within(dialog).getByLabelText(/Display name/));
		await user.type(within(dialog).getByLabelText(/Display name/), 'Orders team');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Save changes' }));
		const alert = await within(dialog).findByRole('alert');
		expect(alert).toHaveTextContent(`Someone else changed ${ID}`);
		expect(within(alert).getByRole('button', { name: 'Reload' })).toBeInTheDocument();
		expect(within(dialog).getByLabelText(/Reason/)).toHaveValue(REASON);
	});

	it('an emptied display name is refused before sending', async () => {
		const seen = capturePatch();
		const { user, dialog } = await openEdit();
		await user.clear(within(dialog).getByLabelText(/Display name/));
		await user.tab();
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		expect(within(dialog).getByRole('button', { name: 'Save changes' })).toBeDisabled();
		expect(within(dialog).getByLabelText(/Display name/)).toHaveAccessibleDescription(
			'Enter a display name.'
		);
		expect(seen).toEqual([]);
	});
});

describe('disable / enable', () => {
	it('disable needs the tenant id typed and sends disabled: true with If-Match', async () => {
		const seen = capturePatch({
			...contractMocks.updateTenant.example,
			tenant: { ...contractMocks.updateTenant.example.tenant, disabled: true }
		});
		const user = userEvent.setup();
		const head = await renderDetail();
		await user.click(within(head).getByRole('button', { name: 'Disable tenant' }));
		const dialog = await screen.findByRole('dialog', { name: `Disable tenant ${ID}` });
		expect(dialog).toHaveTextContent('2 active API keys answers 401');
		const confirm = within(dialog).getByRole('button', { name: 'Disable' });
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByLabelText(/Type .* to confirm/), ID);
		await user.click(confirm);
		expect(await screen.findByText(/Disabled orders-service/)).toBeInTheDocument();
		expect(seen).toEqual([{ body: { disabled: true, reason: REASON }, ifMatch: '"3"' }]);
	});

	it('a disabled tenant offers Enable (no typed confirmation)', async () => {
		server.use(
			http.get(mswPath('/tenants/:tenant_id'), () =>
				HttpResponse.json({ ...tenant, disabled: true }, { headers: { ETag: '"4"' } })
			)
		);
		const seen = capturePatch();
		const user = userEvent.setup();
		const head = await renderDetail();
		expect(within(head).getByRole('note')).toHaveTextContent('answer 401 until it is enabled');
		await user.click(within(head).getByRole('button', { name: 'Enable tenant' }));
		const dialog = await screen.findByRole('dialog', { name: `Enable tenant ${ID}` });
		expect(within(dialog).queryByLabelText(/to confirm/)).toBeNull();
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Enable' }));
		await waitFor(() =>
			expect(seen).toEqual([{ body: { disabled: false, reason: REASON }, ifMatch: '"4"' }])
		);
	});

	it('the action re-checks the typed id', async () => {
		const f = new FormData();
		f.set('reason', REASON);
		f.set('disabled', 'true');
		f.set('confirm', 'billing');
		expect(await runAction(actions, 'setDisabled', f, { params: { tenant_id: ID } })).toMatchObject(
			{
				type: 'failure',
				data: { fields: { confirm: `Type ${ID} to confirm.` } }
			}
		);
	});
});

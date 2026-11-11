import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mswPath } from '$lib/api/mocks/handlers';
import { presets } from '$lib/api/mocks/scenarios';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import type { Role } from '$lib/api/types';
import { useActions } from '$lib/testing/forms';
import ListPage from './+page.svelte';
import { load as listLoad } from './+page';
import { actions as listActions } from './+page.server';
import DetailPage from './[tenant_id]/+page.svelte';
import { load as detailLoad } from './[tenant_id]/+page';
import { actions as detailActions } from './[tenant_id]/+page.server';

// HUB-485: tenants that come from ISFC (contract 2.1.0): read-only ISFC fields, setup pending,
// last synced, "Sync now", no "Create tenant" in ISFC mode, and the "Complete setup" flow.

vi.mock('$app/navigation', () => ({ goto: vi.fn(), invalidateAll: vi.fn() }));

const ID = 'orders-service';
const REASON = 'Finished after the ISFC link, OPS-7';
const ADMIN_URL = 'https://isfc.example.com/admin';

async function renderList(
	access: Access = accessFromRoles(['admin']),
	opts: { isfcMode?: boolean } = {}
) {
	const parent = { access, isfcMode: opts.isfcMode ?? true, isfcAdminUrl: ADMIN_URL };
	const data = await listLoad({
		fetch,
		url: new URL('http://console.test/tenants'),
		parent: async () => parent
	} as never);
	render(ListPage, { props: { data: { ...parent, ...(data as object) } as never } });
	await screen.findByRole('table', { busy: false });
}

async function renderDetail(access: Access = accessFromRoles(['admin'])) {
	const parent = { access, isfcMode: true, isfcAdminUrl: ADMIN_URL };
	const data = await detailLoad({
		fetch,
		url: new URL(`http://console.test/tenants/${ID}`),
		params: { tenant_id: ID },
		parent: async () => parent
	} as never);
	render(DetailPage, { props: { data: { ...parent, ...(data as object) } as never } });
	return screen.findByRole('region', { name: 'Summary' });
}

beforeEach(() => {
	server.events.removeAllListeners();
});

describe('tenant list in ISFC mode', () => {
	beforeEach(() => useActions(listActions));

	it('hides "Create tenant" and shows the ISFC key, state and last synced of each row', async () => {
		await renderList();
		const table = screen.getByRole('table');
		expect(screen.queryByRole('button', { name: 'Create tenant' })).toBeNull();
		expect(within(table).getByText('ORDERS')).toBeInTheDocument();
		expect(within(table).getByText('Active in ISFC')).toBeInTheDocument();
		expect(within(table).getByText(/Synced 2026-10-30 11:55 UTC/)).toBeInTheDocument();
		const note = screen.getByTestId('isfc-note');
		expect(note).toHaveTextContent('read-only copies');
		expect(note).toHaveTextContent('Last synced 2026-10-30 11:55 UTC');
		expect(within(note).getByRole('link', { name: 'in ISFC' })).toHaveAttribute('href', ADMIN_URL);
	});

	it('keeps "Create tenant" outside ISFC mode, and no Sync now when no tenant is from ISFC', async () => {
		server.use(
			http.get(mswPath('/tenants'), () =>
				HttpResponse.json({
					items: contractMocks.listTenants.example.items.map((t) => ({ ...t, isfc: null })),
					next_cursor: null
				})
			)
		);
		await renderList(accessFromRoles(['admin']), { isfcMode: false });
		expect(screen.getByRole('button', { name: 'Create tenant' })).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Sync now' })).toBeNull();
		expect(screen.queryByTestId('isfc-note')).toBeNull();
		expect(screen.getByText('Hub only')).toBeInTheDocument();
	});

	it('badges a tenant whose setup is pending', async () => {
		server.use(...presets['tenant-pending']!());
		await renderList();
		expect(within(screen.getByRole('table')).getByText('Setup pending')).toBeInTheDocument();
	});

	it.each<Role>(['editor', 'viewer'])('%s does not see "Sync now" (admin only)', async (role) => {
		await renderList(accessFromRoles([role]));
		expect(screen.queryByRole('button', { name: 'Sync now' })).toBeNull();
	});

	it('Sync now needs a reason, calls syncTenants and shows what changed with the change id', async () => {
		let sent: unknown;
		server.use(
			http.post(mswPath('/tenants/sync'), async ({ request }) => {
				sent = await request.json();
				return HttpResponse.json(contractMocks.syncTenants.example);
			})
		);
		const user = userEvent.setup();
		await renderList();
		await user.click(screen.getByRole('button', { name: 'Sync now' }));
		const dialog = await screen.findByRole('dialog', { name: 'Sync tenants from ISFC' });
		const confirm = within(dialog).getByRole('button', { name: 'Sync now' });
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(confirm);
		const notice = await screen.findByText(/Synced from ISFC: 1 tenant created/);
		expect(notice.closest('[role="status"]')).toHaveTextContent('chg_01J9Z4B2C3D4E5F6G7H8J9K0NC');
		expect(sent).toEqual({ reason: REASON });
	});

	it('a sync that could not reach ISFC is a warning, not a success', async () => {
		server.use(...presets['sync-failed']!());
		const user = userEvent.setup();
		await renderList();
		await user.click(screen.getByRole('button', { name: 'Sync now' }));
		const dialog = await screen.findByRole('dialog', { name: 'Sync tenants from ISFC' });
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Sync now' }));
		const notice = await screen.findByText(/could not finish: ISFC auth-service did not answer/);
		expect(notice).toHaveTextContent('nothing was disabled');
	});

	it('409 while another sync runs stays in the dialog', async () => {
		server.use(...presetsFor409());
		const user = userEvent.setup();
		await renderList();
		await user.click(screen.getByRole('button', { name: 'Sync now' }));
		const dialog = await screen.findByRole('dialog', { name: 'Sync tenants from ISFC' });
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Sync now' }));
		expect(await within(dialog).findByRole('alert')).toHaveTextContent('Another sync is running');
	});
});

function presetsFor409() {
	return [
		http.post(mswPath('/tenants/sync'), () =>
			HttpResponse.json(
				{
					error: {
						...contractMocks.syncTenants.errors['409']!.error,
						message: 'Another sync is running'
					}
				},
				{ status: 409 }
			)
		)
	];
}

describe('tenant detail of an ISFC tenant', () => {
	beforeEach(() => useActions(detailActions, { params: { tenant_id: ID } }));

	it('shows the ISFC fields read-only, with last synced and a link to change them in ISFC', async () => {
		await renderDetail();
		const isfc = await screen.findByRole('region', { name: 'ISFC' });
		expect(isfc).toHaveTextContent('ORDERS');
		expect(isfc).toHaveTextContent('Orders service');
		expect(isfc).toHaveTextContent('6f1c0b2e-3a4d-4c5e-8f60-1a2b3c4d5e6f');
		expect(isfc).toHaveTextContent('2026-10-30 11:55 UTC');
		expect(within(isfc).getByText('Active in ISFC')).toBeInTheDocument();
		expect(within(isfc).getByRole('link', { name: 'ISFC' })).toHaveAttribute('href', ADMIN_URL);
		// Nothing in the ISFC block can be edited.
		expect(within(isfc).queryByRole('textbox')).toBeNull();
		expect(within(isfc).queryByRole('button', { name: /edit/i })).toBeNull();
	});

	it('a tenant that is not linked to ISFC has no ISFC block', async () => {
		server.use(
			http.get(mswPath('/tenants/:tenant_id'), () =>
				HttpResponse.json({ ...contractMocks.getTenant.example, isfc: null })
			)
		);
		await renderDetail();
		expect(screen.queryByRole('region', { name: 'ISFC' })).toBeNull();
	});

	it('says "Disabled in ISFC" when ISFC is why the tenant is disabled', async () => {
		server.use(
			http.get(mswPath('/tenants/:tenant_id'), () =>
				HttpResponse.json({
					...contractMocks.getTenant.example,
					disabled: true,
					isfc: { ...contractMocks.getTenant.example.isfc!, status: 'INACTIVE' }
				})
			)
		);
		const head = await renderDetail();
		expect(within(head).getByRole('note')).toHaveTextContent('Disabled in ISFC');
		expect(await screen.findByText('Inactive in ISFC')).toBeInTheDocument();
	});

	it('Sync now calls syncTenant for this tenant (admin only)', async () => {
		let sent: unknown;
		server.use(
			http.post(mswPath('/tenants/:tenant_id/sync'), async ({ request, params }) => {
				sent = { id: params.tenant_id, body: await request.json() };
				return HttpResponse.json(contractMocks.syncTenant.example);
			})
		);
		const user = userEvent.setup();
		await renderDetail();
		const isfc = await screen.findByRole('region', { name: 'ISFC' });
		await user.click(within(isfc).getByRole('button', { name: 'Sync now' }));
		const dialog = await screen.findByRole('dialog', { name: `Sync ${ID} from ISFC` });
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Sync now' }));
		expect(await screen.findByText(`Refreshed ${ID} from ISFC.`)).toBeInTheDocument();
		expect(sent).toEqual({ id: ID, body: { reason: REASON } });
	});

	it('a tenant admin has no Sync now and no Disable, but can edit', async () => {
		const head = await renderDetail(accessFromRoles(['tenant_admin'], [ID]));
		const isfc = await screen.findByRole('region', { name: 'ISFC' });
		expect(within(isfc).queryByRole('button', { name: 'Sync now' })).toBeNull();
		expect(within(head).queryByRole('button', { name: /Disable tenant/ })).toBeNull();
		expect(within(head).getByRole('button', { name: 'Edit tenant' })).toBeInTheDocument();
	});
});

describe('complete setup', () => {
	beforeEach(() => {
		useActions(detailActions, { params: { tenant_id: ID } });
		server.use(...presets['tenant-pending']!());
	});

	it('shows the checklist and a badge while setup is pending', async () => {
		const head = await renderDetail();
		expect(within(head).getByText('Setup pending')).toBeInTheDocument();
		const panel = await screen.findByRole('region', { name: /Finish setting up this tenant/ });
		expect(panel).toHaveTextContent('Display name:');
		expect(panel).toHaveTextContent('200 requests/s, burst 400');
		await waitFor(() => expect(panel).toHaveTextContent(/configured, below|none yet/));
		expect(within(panel).getByRole('button', { name: 'Complete setup' })).toBeInTheDocument();
	});

	it('is not shown once setup is complete', async () => {
		server.resetHandlers();
		await renderDetail();
		expect(screen.queryByRole('region', { name: /Finish setting up/ })).toBeNull();
	});

	it('a viewer cannot complete setup and is told which role can', async () => {
		await renderDetail(accessFromRoles(['viewer']));
		const panel = await screen.findByRole('region', { name: /Finish setting up this tenant/ });
		expect(within(panel).queryByRole('button', { name: 'Complete setup' })).toBeNull();
		expect(panel).toHaveTextContent('Tenant admin role can complete setup');
	});

	it('a tenant admin sends the changed fields plus setup: complete (never disabled)', async () => {
		const seen: { body: unknown; ifMatch: string | null }[] = [];
		server.use(
			http.patch(mswPath('/tenants/:tenant_id'), async ({ request }) => {
				seen.push({ body: await request.json(), ifMatch: request.headers.get('If-Match') });
				return HttpResponse.json(contractMocks.updateTenant.example);
			})
		);
		const user = userEvent.setup();
		await renderDetail(accessFromRoles(['tenant_admin'], [ID]));
		const panel = await screen.findByRole('region', { name: /Finish setting up this tenant/ });
		await user.click(within(panel).getByRole('button', { name: 'Complete setup' }));
		const dialog = await screen.findByRole('dialog', { name: `Complete setup of ${ID}` });
		const confirm = within(dialog).getByRole('button', { name: 'Complete setup' });
		expect(confirm).toBeDisabled();
		const name = within(dialog).getByLabelText(/Display name/);
		await user.clear(name);
		await user.type(name, 'Orders team');
		const rps = within(dialog).getByLabelText('Requests per second');
		await user.clear(rps);
		await user.type(rps, '300');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(confirm);
		expect(await screen.findByText(/Setup of orders-service is complete/)).toBeInTheDocument();
		expect(seen).toHaveLength(1);
		expect(seen[0]!.body).toEqual({
			display_name: 'Orders team',
			rate_limit: { rps: 300 },
			setup: 'complete',
			reason: REASON
		});
		expect(seen[0]!.body).not.toHaveProperty('disabled');
	});

	it('can complete setup with nothing changed (just the reason)', async () => {
		let sent: unknown;
		server.use(
			http.patch(mswPath('/tenants/:tenant_id'), async ({ request }) => {
				sent = await request.json();
				return HttpResponse.json(contractMocks.updateTenant.example);
			})
		);
		const user = userEvent.setup();
		await renderDetail();
		const panel = await screen.findByRole('region', { name: /Finish setting up this tenant/ });
		await user.click(within(panel).getByRole('button', { name: 'Complete setup' }));
		const dialog = await screen.findByRole('dialog', { name: `Complete setup of ${ID}` });
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Complete setup' }));
		await screen.findByText(/Setup of orders-service is complete/);
		expect(sent).toEqual({ setup: 'complete', reason: REASON });
	});
});

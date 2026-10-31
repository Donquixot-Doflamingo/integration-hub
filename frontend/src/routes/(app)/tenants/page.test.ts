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

// HUB-270: tenants list (search, paging), tenant viewers' own tenants, create with reason.

const goto = vi.fn();
vi.mock('$app/navigation', () => ({
	goto: (...a: unknown[]) => goto(...a),
	invalidateAll: vi.fn()
}));

const REASON = 'Pilot tenant for the orders team';

async function renderAt(search: string, access: Access = accessFromRoles(['admin'])) {
	const data = await load({
		fetch,
		url: new URL(`http://console.test/tenants${search}`),
		parent: async () => ({ access })
	} as never);
	return render(Page, { props: { data: { access, ...(data as object) } as never } });
}

function seen(path: RegExp) {
	const urls: URL[] = [];
	server.events.on('request:start', ({ request }) => {
		const u = new URL(request.url);
		if (path.test(u.pathname)) urls.push(u);
	});
	return urls;
}

beforeEach(() => {
	goto.mockReset();
	server.events.removeAllListeners();
	useActions(actions);
});

describe('tenants list', () => {
	it('sends the search and links each tenant to its page', async () => {
		const calls = seen(/\/admin\/v1\/tenants$/);
		await renderAt('?q=orders');
		const table = await screen.findByRole('table', { busy: false });
		expect(Object.fromEntries(calls[0]!.searchParams)).toEqual({ q: 'orders', limit: '50' });
		expect(within(table).getByRole('link', { name: 'orders-service' })).toHaveAttribute(
			'href',
			'/tenants/orders-service'
		);
		expect(screen.getByRole('link', { name: 'Clear search' })).toHaveAttribute('href', '/tenants');
		expect(screen.getByRole('navigation', { name: 'Pagination' })).toBeInTheDocument();
	});

	it('applies the search form as a URL', async () => {
		await renderAt('');
		await screen.findByRole('table', { busy: false });
		await userEvent.type(screen.getByLabelText('Search'), 'billing');
		await userEvent.click(screen.getByRole('button', { name: 'Search' }));
		expect(goto).toHaveBeenCalledWith('/tenants?q=billing', { keepFocus: true, noScroll: true });
	});

	it('says when the search hides everything', async () => {
		server.use(
			http.get(mswPath('/tenants'), () => HttpResponse.json({ items: [], next_cursor: null }))
		);
		await renderAt('?q=nothing');
		expect(await screen.findByText('No tenants match this search')).toBeInTheDocument();
		expect(screen.getByText(/Nothing matches "nothing"/)).toBeInTheDocument();
	});
});

describe('tenant viewers', () => {
	it('see only their own tenants, read with getTenant (listTenants needs viewer)', async () => {
		const list = seen(/\/admin\/v1\/tenants$/);
		const reads = seen(/\/admin\/v1\/tenants\/[^/]+$/);
		server.use(
			http.get(mswPath('/tenants/:tenant_id'), ({ params }) =>
				HttpResponse.json({
					...contractMocks.getTenant.example,
					tenant_id: params.tenant_id as string,
					display_name: `Tenant ${params.tenant_id}`
				})
			)
		);
		await renderAt('', accessFromRoles(['tenant_viewer'], ['orders-service', 'billing']));
		const table = await screen.findByRole('table', { busy: false });
		expect(list).toHaveLength(0);
		expect(reads.map((u) => u.pathname.split('/').pop()).sort()).toEqual([
			'billing',
			'orders-service'
		]);
		expect(
			within(table)
				.getAllByRole('rowheader')
				.map((h) => h.textContent?.trim())
		).toEqual(['orders-service', 'billing']);
		expect(screen.getByRole('note')).toHaveTextContent('tenants your role is limited to');
		expect(screen.queryByRole('navigation', { name: 'Pagination' })).toBeNull();
		expect(screen.queryByRole('button', { name: 'Create tenant' })).toBeNull();
	});

	it('search their tenants in the browser', async () => {
		server.use(
			http.get(mswPath('/tenants/:tenant_id'), ({ params }) =>
				HttpResponse.json({
					...contractMocks.getTenant.example,
					tenant_id: params.tenant_id as string,
					display_name: `Tenant ${params.tenant_id}`
				})
			)
		);
		await renderAt('?q=bill', accessFromRoles(['tenant_viewer'], ['orders-service', 'billing']));
		const table = await screen.findByRole('table', { busy: false });
		expect(
			within(table)
				.getAllByRole('rowheader')
				.map((h) => h.textContent?.trim())
		).toEqual(['billing']);
	});

	it('list the tenants that could not be read', async () => {
		server.use(
			http.get(mswPath('/tenants/:tenant_id'), ({ params }) =>
				params.tenant_id === 'gone'
					? HttpResponse.json(contractMocks.getTenant.errors['404'], { status: 404 })
					: HttpResponse.json(contractMocks.getTenant.example)
			)
		);
		await renderAt('', accessFromRoles(['tenant_viewer'], ['orders-service', 'gone']));
		await screen.findByRole('table', { busy: false });
		const note = await screen.findByText(/could not be loaded/);
		expect(note.closest('[role="note"]')).toHaveTextContent('gone: NOT_FOUND');
	});
});

describe('create tenant', () => {
	async function openCreate() {
		const user = userEvent.setup();
		await renderAt('');
		await screen.findByRole('table', { busy: false });
		await user.click(screen.getByRole('button', { name: 'Create tenant' }));
		return { user, dialog: await screen.findByRole('dialog', { name: 'Create a tenant' }) };
	}

	it.each<Role>(['editor', 'viewer'])('is admin only (%s sees no button)', async (role) => {
		await renderAt('', accessFromRoles([role]));
		await screen.findByRole('table', { busy: false });
		expect(screen.queryByRole('button', { name: 'Create tenant' })).toBeNull();
	});

	it('checks the id and name, needs a reason, then sends TenantCreate and shows the change id', async () => {
		let sent: unknown;
		server.use(
			http.post(mswPath('/tenants'), async ({ request }) => {
				sent = await request.json();
				return HttpResponse.json(contractMocks.createTenant.example, { status: 201 });
			})
		);
		const { user, dialog } = await openCreate();
		const confirm = within(dialog).getByRole('button', { name: 'Create tenant' });
		await user.type(within(dialog).getByLabelText(/Tenant id/), 'no');
		await user.tab();
		expect(within(dialog).getByLabelText(/Tenant id/)).toHaveAccessibleDescription(
			/3 to 64 characters/
		);
		await user.clear(within(dialog).getByLabelText(/Tenant id/));
		await user.type(within(dialog).getByLabelText(/Tenant id/), 'orders-service');
		await user.type(within(dialog).getByLabelText(/Display name/), 'Orders service');
		await user.type(within(dialog).getByLabelText('Requests per second'), '200');
		await user.type(within(dialog).getByLabelText('Burst'), '400');
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		expect(confirm).toBeEnabled();
		await user.click(confirm);
		const notice = await screen.findByText(/Created tenant orders-service \(Orders service\)/);
		expect(notice.closest('[role="status"]')).toHaveTextContent('chg_01J9Z4B2C3D4E5F6G7H8J9K0NA');
		expect(screen.getByRole('link', { name: 'Open orders-service' })).toHaveAttribute(
			'href',
			'/tenants/orders-service'
		);
		expect(sent).toEqual({
			tenant_id: 'orders-service',
			display_name: 'Orders service',
			description: null,
			contact: null,
			rate_limit: { rps: 200, burst: 400 },
			reason: REASON
		});
	});

	it('puts an API pointer on its field', async () => {
		server.use(
			mockError('createTenant', 400, {
				details: [{ pointer: '/tenant_id', keyword: 'pattern', message: 'must match Slug' }]
			})
		);
		const { user, dialog } = await openCreate();
		await user.type(within(dialog).getByLabelText(/Tenant id/), 'orders-service');
		await user.type(within(dialog).getByLabelText(/Display name/), 'Orders');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Create tenant' }));
		await waitFor(() =>
			expect(within(dialog).getByLabelText(/Tenant id/)).toHaveAccessibleDescription(
				/must match Slug/
			)
		);
	});

	it('409: the id is taken', async () => {
		server.use(mockError('createTenant', 409, { message: 'Tenant orders-service exists' }));
		const { user, dialog } = await openCreate();
		await user.type(within(dialog).getByLabelText(/Tenant id/), 'orders-service');
		await user.type(within(dialog).getByLabelText(/Display name/), 'Orders');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Create tenant' }));
		expect(await within(dialog).findByRole('alert')).toHaveTextContent(
			'Tenant orders-service exists'
		);
	});
});

describe('create action (no-JS path)', () => {
	it('repeats the checks and calls nothing on bad input', async () => {
		const calls = seen(/\/admin\/v1\/tenants$/);
		const f = new FormData();
		f.set('reason', REASON);
		f.set('tenant_id', 'a b');
		f.set('display_name', '');
		const r = await runAction(actions, 'create', f);
		expect(r).toMatchObject({
			type: 'failure',
			status: 400,
			data: { fields: { tenant_id: expect.any(String), display_name: 'Enter a display name.' } }
		});
		expect(calls).toHaveLength(0);
	});

	it('requires a reason', async () => {
		const f = new FormData();
		f.set('tenant_id', 'orders-service');
		f.set('display_name', 'Orders');
		expect(await runAction(actions, 'create', f)).toMatchObject({
			type: 'failure',
			data: { fields: { reason: expect.stringMatching(/Enter a reason/) } }
		});
	});
});

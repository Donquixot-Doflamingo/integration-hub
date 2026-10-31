import { render, screen, within } from '@testing-library/svelte';
import { http } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockOperation, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import type { Role } from '$lib/api/types';
import Page from './+page.svelte';
import { load } from './+page';

// HUB-256: integrations list against MSW (contract examples).

const goto = vi.fn();
vi.mock('$app/navigation', () => ({
	goto: (...a: unknown[]) => goto(...a),
	invalidateAll: vi.fn()
}));

const example = contractMocks.listIntegrations.example;
const base = example.items[0]!;

async function renderAt(search: string, access: Access = accessFromRoles(['editor'])) {
	const data = load({
		fetch,
		url: new URL(`http://console.test/integrations${search}`)
	} as never) as object;
	return render(Page, { props: { data: { access, ...data } as never } });
}

/** Records the query of every listIntegrations call. */
function recordQueries() {
	const seen: URLSearchParams[] = [];
	server.events.on('request:start', ({ request }) => {
		const u = new URL(request.url);
		if (u.pathname.endsWith('/admin/v1/integrations')) seen.push(u.searchParams);
	});
	return seen;
}

beforeEach(() => {
	goto.mockReset();
	server.events.removeAllListeners();
});

describe('integrations list', () => {
	it('sends the URL filters to listIntegrations and shows them as removable chips', async () => {
		const seen = recordQueries();
		await renderAt(
			'?q=brand&tenant_id=orders-service&capability=events.track@v1&paused=false&limit=25'
		);
		await screen.findByRole('table', { busy: false });
		expect(Object.fromEntries(seen[0]!)).toEqual({
			q: 'brand',
			tenant_id: 'orders-service',
			capability: 'events.track@v1',
			paused: 'false',
			limit: '25'
		});
		const chips = screen.getByRole('list', { name: 'Active filters' });
		const remove = within(chips).getByRole('link', {
			name: 'Remove filter Owner tenant: orders-service'
		});
		expect(remove).toHaveAttribute(
			'href',
			'/integrations?q=brand&capability=events.track%40v1&paused=false&limit=25'
		);
		expect(
			within(chips).getByRole('link', { name: 'Remove filter State: Live only' })
		).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Clear all' })).toHaveAttribute(
			'href',
			'/integrations?limit=25'
		);
		// The form shows the current filters.
		expect(screen.getByLabelText('Search')).toHaveValue('brand');
		expect(screen.getByLabelText('State')).toHaveValue('false');
		expect(screen.getByLabelText('Per page')).toHaveValue('25');
	});

	it('sends the active-version filters and shows them as chips (HUB-283)', async () => {
		const seen = recordQueries();
		await renderAt('?has_active_version=true&active_version_behind=true');
		await screen.findByRole('table', { busy: false });
		expect(Object.fromEntries(seen[0]!)).toEqual({
			has_active_version: 'true',
			active_version_behind: 'true',
			limit: '50'
		});
		const chips = screen.getByRole('list', { name: 'Active filters' });
		expect(
			within(chips).getByRole('link', { name: 'Remove filter Active version: Published' })
		).toHaveAttribute('href', '/integrations?active_version_behind=true');
		expect(
			within(chips).getByRole('link', { name: 'Remove filter Newer version or draft: Waiting' })
		).toHaveAttribute('href', '/integrations?has_active_version=true');
		expect(screen.getByLabelText('Active version')).toHaveValue('true');
		expect(screen.getByLabelText(/^Newer version or draft/)).toHaveValue('true');
		expect(screen.queryByRole('note')).toBeNull();
	});

	it('puts the active-version selects in the URL', async () => {
		await renderAt('');
		await screen.findByRole('table', { busy: false });
		(screen.getByLabelText('Active version') as HTMLSelectElement).value = 'false';
		screen.getByRole('button', { name: 'Apply' }).click();
		expect(goto).toHaveBeenCalledWith('/integrations?has_active_version=false', {
			keepFocus: true,
			noScroll: true
		});
	});

	it('explains why "no active version" plus "newer version" lists nothing', async () => {
		server.use(mockOperation('listIntegrations', { items: [], next_cursor: null }));
		await renderAt('?has_active_version=false&active_version_behind=true');
		expect(await screen.findByRole('note')).toHaveTextContent(/together match nothing/);
	});

	it('applies the form as a URL without empty fields, back on the first page', async () => {
		await renderAt('?cursor=c9&prev=');
		await screen.findByRole('table', { busy: false });
		const search = screen.getByLabelText('Search');
		search.focus();
		(search as HTMLInputElement).value = 'brand';
		(screen.getByLabelText('State') as HTMLSelectElement).value = 'true';
		screen.getByRole('button', { name: 'Apply' }).click();
		expect(goto).toHaveBeenCalledWith('/integrations?q=brand&paused=true', {
			keepFocus: true,
			noScroll: true
		});
	});

	it('marks paused integrations and those without an active version, in text', async () => {
		server.use(
			mockOperation('listIntegrations', {
				items: [
					{ ...base, integration_id: 'int_live', paused: false },
					{ ...base, integration_id: 'int_paused', paused: true },
					{
						...base,
						integration_id: 'int_new',
						active_version: null,
						latest_version: 1,
						has_draft: true,
						targets: []
					}
				],
				next_cursor: null
			})
		);
		await renderAt('');
		const table = await screen.findByRole('table', { busy: false });
		const rows = within(table).getAllByRole('row').slice(1);
		expect(rows[0]).toHaveTextContent('Live');
		expect(rows[1]).toHaveTextContent('Paused');
		expect(rows[2]).toHaveTextContent('No active version');
		expect(rows[2]).toHaveTextContent('v1draft');
	});

	it('pages with the cursor and keeps a way back', async () => {
		server.use(mockOperation('listIntegrations', { ...example, next_cursor: 'c2' }));
		await renderAt('?tenant_id=orders-service&cursor=c1&prev=');
		await screen.findByRole('table', { busy: false });
		const nav = screen.getByRole('navigation', { name: 'Pagination' });
		expect(nav).toHaveTextContent('Page 2: 1 integration, more after this page.');
		expect(within(nav).getByRole('link', { name: 'Next' })).toHaveAttribute(
			'href',
			'/integrations?tenant_id=orders-service&cursor=c2&prev=&prev=c1'
		);
		expect(within(nav).getByRole('link', { name: 'Previous' })).toHaveAttribute(
			'href',
			'/integrations?tenant_id=orders-service'
		);
	});

	it('has no Previous on the first page and no Next on the last', async () => {
		await renderAt('');
		await screen.findByRole('table', { busy: false });
		const nav = screen.getByRole('navigation', { name: 'Pagination' });
		expect(within(nav).queryByRole('link')).toBeNull();
		expect(nav).toHaveTextContent('Page 1: 1 integration.');
	});

	it('says when filters hide everything, with a way out', async () => {
		server.use(mockOperation('listIntegrations', { items: [], next_cursor: null }));
		await renderAt('?paused=true');
		expect(await screen.findByText('No integrations match these filters')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Clear all filters' })).toHaveAttribute(
			'href',
			'/integrations'
		);
	});

	it.each<Role>(['admin', 'editor', 'viewer'])(
		'%s gets vendor and account filters',
		async (role) => {
			await renderAt('', accessFromRoles([role]));
			expect(screen.getByLabelText('Vendor')).toBeInTheDocument();
			expect(screen.getByLabelText('Owner tenant').tagName).toBe('INPUT');
		}
	);

	it('a tenant viewer picks among their tenants and gets no vendor or account filters', async () => {
		await renderAt(
			'?tenant_id=billing',
			accessFromRoles(['tenant_viewer'], ['orders-service', 'billing'])
		);
		const tenant = screen.getByLabelText('Owner tenant');
		expect(tenant.tagName).toBe('SELECT');
		expect(
			within(tenant)
				.getAllByRole('option')
				.map((o) => o.textContent)
		).toEqual(['All my tenants', 'orders-service', 'billing']);
		expect(tenant).toHaveValue('billing');
		expect(screen.queryByLabelText('Vendor')).toBeNull();
		expect(screen.queryByLabelText('Account')).toBeNull();
	});

	it('shows a 400 from the API with its details and request id', async () => {
		server.use(
			http.get(mswPath('/integrations'), () =>
				Response.json(
					{
						error: {
							code: 'VALIDATION_FAILED',
							message: 'Invalid query',
							retryable: false,
							details: [{ pointer: '/capability', message: 'does not match pattern' }],
							request_id: 'req_01J9Z3M1A5B7C9D1E3F5G7H9KC'
						}
					},
					{ status: 400 }
				)
			)
		);
		await renderAt('?capability=nope');
		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent('VALIDATION_FAILED');
		expect(alert).toHaveTextContent('/capability does not match pattern');
		expect(alert).toHaveTextContent('req_01J9Z3M1A5B7C9D1E3F5G7H9KC');
	});
});

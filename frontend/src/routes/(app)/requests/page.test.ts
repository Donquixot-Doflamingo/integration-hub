import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mockOperation, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import Page from './+page.svelte';
import { load } from './+page';

// HUB-263: request explorer search against MSW.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

beforeEach(() => vi.mocked(goto).mockReset());

function renderAt(qs = '', access: Access = accessFromRoles(['viewer'])) {
	const data = load({
		fetch,
		url: new URL(`http://console.test/requests${qs}`)
	} as never) as object;
	return render(Page, { props: { data: { access, ...data } as never } });
}

function captureUrl() {
	const urls: URL[] = [];
	server.use(
		http.get(mswPath('/requests'), ({ request }) => {
			urls.push(new URL(request.url));
			return HttpResponse.json(contractMocks.searchRequests.example);
		})
	);
	return urls;
}

describe('request explorer', () => {
	it('sends the URL filters to searchRequests (status and code comma-separated)', async () => {
		const urls = captureUrl();
		renderAt(
			'?tenant_id=orders-service&status=dead_lettered,parked&code=UPSTREAM_TIMEOUT,UPSTREAM_REJECTED&from=2026-10-31T00:00:00Z&to=2026-11-01T00:00:00Z'
		);
		await screen.findByRole('table', { busy: false });
		const q = urls[0]!.searchParams;
		expect(q.get('status')).toBe('dead_lettered,parked');
		expect(q.getAll('status')).toHaveLength(1);
		expect(q.get('code')).toBe('UPSTREAM_TIMEOUT,UPSTREAM_REJECTED');
		expect(q.get('tenant_id')).toBe('orders-service');
		expect(q.get('from')).toBe('2026-10-31T00:00:00Z');
		expect(q.get('to')).toBe('2026-11-01T00:00:00Z');
	});

	it('shows the filters in the form and as removable chips', async () => {
		renderAt('?status=parked&integration_id=int_evt_brandA&from=2026-10-31T00:00:00Z');
		expect(screen.getByRole('checkbox', { name: 'Parked' })).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Failed' })).not.toBeChecked();
		expect(screen.getByLabelText('Integration')).toHaveValue('int_evt_brandA');
		expect(screen.getByLabelText('From (UTC)')).toHaveValue('2026-10-31T00:00');
		const chips = screen.getByRole('list', { name: 'Active filters' });
		const remove = within(chips).getByRole('link', { name: 'Remove filter Status: Parked' });
		expect(remove).toHaveAttribute(
			'href',
			'/requests?integration_id=int_evt_brandA&from=2026-10-31T00%3A00%3A00Z'
		);
		expect(screen.getByRole('link', { name: 'Clear all' })).toHaveAttribute('href', '/requests');
	});

	it('the search form navigates to a clean, shareable URL on the first page', async () => {
		const user = userEvent.setup();
		renderAt('?cursor=abc&prev=');
		await user.type(screen.getByLabelText('Idempotency key'), 'order-8821-created');
		await user.click(screen.getByRole('checkbox', { name: 'Dead-lettered' }));
		await user.type(screen.getByLabelText(/^Error codes/), 'upstream_timeout');
		await user.click(screen.getByRole('button', { name: 'Search' }));
		expect(goto).toHaveBeenCalledWith(
			'/requests?idempotency_key=order-8821-created&status=dead_lettered&code=UPSTREAM_TIMEOUT',
			expect.anything()
		);
	});

	it('pages with the cursor and keeps the filters', async () => {
		renderAt('?status=succeeded');
		await screen.findByRole('table', { busy: false });
		expect(screen.getByRole('link', { name: /Next/ })).toHaveAttribute(
			'href',
			`/requests?status=succeeded&cursor=${contractMocks.searchRequests.example.next_cursor}&prev=`
		);
	});

	it('does not call the API for a range over 31 days and says why', async () => {
		server.use(
			http.get(mswPath('/requests'), () => {
				throw new Error('must not be called');
			})
		);
		renderAt('?from=2026-09-01T00:00:00Z&to=2026-11-01T00:00:00Z');
		expect(screen.getByRole('alert')).toHaveTextContent('at most 31 days');
		expect(screen.getByLabelText('From (UTC)')).toHaveAttribute('aria-invalid', 'true');
		expect(screen.getByLabelText('To (UTC)')).toHaveAccessibleDescription(/at most 31 days/);
		expect(screen.queryByRole('table')).toBeNull();
	});

	it('offers quick ranges as absolute, shareable links', () => {
		renderAt('?status=failed');
		const link = within(screen.getByRole('list', { name: 'Quick time ranges' })).getByRole('link', {
			name: 'Last hour'
		});
		const url = new URL(link.getAttribute('href')!, 'http://x');
		expect(url.searchParams.get('status')).toBe('failed');
		const span =
			Date.parse(url.searchParams.get('to')!) - Date.parse(url.searchParams.get('from')!);
		expect(span).toBe(3_600_000);
	});

	it('separates "no requests" from "filters hide everything"', async () => {
		server.use(mockOperation('searchRequests', { items: [], next_cursor: null }));
		renderAt('?status=expired');
		expect(await screen.findByText('No requests match this search')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Clear all filters' })).toHaveAttribute(
			'href',
			'/requests'
		);
	});

	it('shows an API validation error with its pointer and request id', async () => {
		server.use(
			mockError('searchRequests', 400, {
				message: 'Invalid query',
				details: [{ pointer: '/to', keyword: 'range', message: 'range must span at most 31 days' }]
			})
		);
		renderAt('?status=failed');
		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent('VALIDATION_FAILED');
		expect(alert).toHaveTextContent('/to range must span at most 31 days');
		expect(alert).toHaveTextContent(contractMocks.searchRequests.errors['400']!.error.request_id);
	});

	it('tenant viewers pick among their tenants and see no vendor or account', async () => {
		renderAt('', accessFromRoles(['tenant_viewer'], ['orders-service', 'billing']));
		const tenant = screen.getByLabelText('Tenant');
		expect(tenant.tagName).toBe('SELECT');
		expect(
			within(tenant)
				.getAllByRole('option')
				.map((o) => o.textContent)
		).toEqual(['All my tenants', 'orders-service', 'billing']);
		expect(screen.queryByLabelText('Vendor')).toBeNull();
		const table = await screen.findByRole('table', { busy: false });
		expect(within(table).queryByRole('columnheader', { name: 'Vendor / account' })).toBeNull();
		expect(table).not.toHaveTextContent('clevertap');
	});
});

import { render, screen, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mockOperation } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import Page from './+page.svelte';
import { load } from './+page';

// HUB-266: DLQ list with filters in the URL, against MSW (contract examples).

const goto = vi.fn();
vi.mock('$app/navigation', () => ({
	goto: (...a: unknown[]) => goto(...a),
	invalidateAll: vi.fn()
}));

const example = contractMocks.listDeadLetters.example;
const entry = example.items[0]!;

async function renderAt(search: string, access: Access = accessFromRoles(['viewer'])) {
	const data = load({ fetch, url: new URL(`http://console.test/dlq${search}`) } as never) as object;
	return render(Page, { props: { data: { access, ...data } as never } });
}

function recordQueries() {
	const seen: URLSearchParams[] = [];
	server.events.on('request:start', ({ request }) => {
		const u = new URL(request.url);
		if (u.pathname.endsWith('/admin/v1/dlq')) seen.push(u.searchParams);
	});
	return seen;
}

beforeEach(() => {
	goto.mockReset();
	server.events.removeAllListeners();
});

describe('DLQ list', () => {
	it('sends the URL filters to listDeadLetters and shows them as removable chips', async () => {
		const seen = recordQueries();
		await renderAt(
			'?integration_id=int_evt_brandA&reason=UPSTREAM_REJECTED&replayed=false&from=2026-10-31T00:00:00Z&limit=25'
		);
		await screen.findByRole('table', { busy: false });
		expect(Object.fromEntries(seen[0]!)).toEqual({
			integration_id: 'int_evt_brandA',
			reason: 'UPSTREAM_REJECTED',
			replayed: 'false',
			from: '2026-10-31T00:00:00Z',
			limit: '25'
		});
		const chips = screen.getByRole('list', { name: 'Active filters' });
		expect(
			within(chips).getByRole('link', { name: 'Remove filter Reason: UPSTREAM_REJECTED' })
		).toHaveAttribute(
			'href',
			'/dlq?integration_id=int_evt_brandA&replayed=false&from=2026-10-31T00%3A00%3A00Z&limit=25'
		);
		expect(
			within(chips).getByRole('link', { name: 'Remove filter Replayed: Not replayed yet' })
		).toBeInTheDocument();
		expect(
			within(chips).getByRole('link', { name: 'Remove filter From: 2026-10-31 00:00 UTC' })
		).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Clear all' })).toHaveAttribute(
			'href',
			'/dlq?limit=25'
		);
		expect(screen.getByLabelText(/^Reason/)).toHaveValue('UPSTREAM_REJECTED');
		expect(screen.getByLabelText('Replayed')).toHaveValue('false');
		expect(screen.getByLabelText('From (UTC)')).toHaveValue('2026-10-31T00:00');
	});

	it('lists each entry with its reason, message and a link to the entry', async () => {
		await renderAt('');
		const table = await screen.findByRole('table', { busy: false });
		const row = within(table).getAllByRole('row')[1]!;
		expect(within(row).getByRole('link', { name: entry.request_id })).toHaveAttribute(
			'href',
			`/dlq/${entry.request_id}`
		);
		expect(row).toHaveTextContent('UPSTREAM_REJECTED');
		expect(row).toHaveTextContent(entry.message!);
		expect(row).toHaveTextContent('clevertap / ct-brandA-in');
		expect(row).toHaveTextContent('Waiting');
		expect(row).toHaveTextContent('2026-10-31 11:02 UTC');
	});

	it('applies the form as a clean URL, back on the first page', async () => {
		await renderAt('?cursor=c9&prev=');
		await screen.findByRole('table', { busy: false });
		(screen.getByLabelText('Account') as HTMLInputElement).value = 'ct-brandA-in';
		(screen.getByLabelText(/^Reason/) as HTMLInputElement).value = 'upstream_rejected';
		screen.getByRole('button', { name: 'Apply' }).click();
		expect(goto).toHaveBeenCalledWith('/dlq?account_id=ct-brandA-in&reason=UPSTREAM_REJECTED', {
			keepFocus: true,
			noScroll: true
		});
	});

	it('opens filtered by account from the health board link', async () => {
		const seen = recordQueries();
		await renderAt('?account_id=ct-brandA-in');
		await screen.findByRole('table', { busy: false });
		expect(seen[0]!.get('account_id')).toBe('ct-brandA-in');
		expect(screen.getByLabelText('Account')).toHaveValue('ct-brandA-in');
	});

	it('refuses a reversed time range without calling the API', async () => {
		const seen = recordQueries();
		await renderAt('?from=2026-10-31T12:00:00Z&to=2026-10-31T09:00:00Z');
		expect(screen.getByRole('alert')).toHaveTextContent('"To" must be later than "From".');
		expect(screen.getByLabelText('From (UTC)')).toHaveAttribute('aria-invalid', 'true');
		expect(screen.queryByRole('table')).toBeNull();
		expect(seen).toHaveLength(0);
	});

	it('says when filters hide everything, with a way out', async () => {
		server.use(mockOperation('listDeadLetters', { items: [], next_cursor: null }));
		await renderAt('?reason=poison');
		expect(await screen.findByText('No dead letters match these filters')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: /Clear all filters/ })).toHaveAttribute('href', '/dlq');
	});

	it('a tenant viewer picks among their tenants and gets no vendor or account', async () => {
		await renderAt('', accessFromRoles(['tenant_viewer'], ['orders-service', 'billing']));
		const table = await screen.findByRole('table', { busy: false });
		const tenant = screen.getByLabelText('Tenant');
		expect(tenant.tagName).toBe('SELECT');
		expect(
			within(tenant)
				.getAllByRole('option')
				.map((o) => o.textContent)
		).toEqual(['All my tenants', 'orders-service', 'billing']);
		expect(screen.queryByLabelText('Vendor')).toBeNull();
		expect(screen.queryByLabelText('Account')).toBeNull();
		expect(within(table).queryByRole('columnheader', { name: 'Vendor / account' })).toBeNull();
		expect(table).not.toHaveTextContent('clevertap');
	});

	it('shows a 400 with its details and request id', async () => {
		server.use(mockError('listDeadLetters', 400));
		await renderAt('?reason=UPSTREAM_REJECTED');
		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent('VALIDATION_FAILED');
		expect(alert).toHaveTextContent(contractMocks.listDeadLetters.errors['400']!.error.request_id);
	});
});

import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import type { Component } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mockOperation, mockPending } from '$lib/api/mocks/handlers';
import type { OperationId } from '$lib/api/mocks/types';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles } from '$lib/auth/roles';

// HUB-254: every section route exists, loads its list operation (MSW, contract examples) and
// shows loading, empty, error and populated states.

const invalidateAll = vi.fn();
vi.mock('$app/navigation', () => ({ invalidateAll: () => invalidateAll(), goto: vi.fn() }));

type LoadFn = (event: { fetch: typeof fetch; url: URL; parent: () => Promise<unknown> }) => unknown;
interface SectionCase {
	path: string;
	heading: string;
	op: OperationId;
	/** Text of the first row header in the contract example. */
	firstRow: string;
	page: () => Promise<{ default: Component<{ data: never }> }>;
	load: () => Promise<{ load: LoadFn }>;
}

const cases: SectionCase[] = [
	{
		path: '/integrations',
		heading: 'Integrations',
		op: 'listIntegrations',
		firstRow: 'int_evt_brandA',
		page: () => import('./integrations/+page.svelte') as never,
		load: () => import('./integrations/+page') as never
	},
	{
		path: '/requests',
		heading: 'Requests',
		op: 'searchRequests',
		firstRow: 'req_01J9Z3K8X2V7Q4M6N8P0R2S4T6',
		page: () => import('./requests/+page.svelte') as never,
		load: () => import('./requests/+page') as never
	},
	{
		path: '/dlq',
		heading: 'DLQ',
		op: 'listDeadLetters',
		firstRow: 'req_01J9Z3M1A5B7C9D1E3F5G7H9JK',
		page: () => import('./dlq/+page.svelte') as never,
		load: () => import('./dlq/+page') as never
	},
	{
		path: '/transformers',
		heading: 'Transformers',
		op: 'listTransformers',
		firstRow: 'T1',
		page: () => import('./transformers/+page.svelte') as never,
		load: () => import('./transformers/+page') as never
	},
	{
		path: '/vendors',
		heading: 'Vendors',
		op: 'listVendors',
		firstRow: 'clevertap',
		page: () => import('./vendors/+page.svelte') as never,
		load: () => import('./vendors/+page') as never
	},
	{
		path: '/accounts',
		heading: 'Accounts',
		op: 'listAccounts',
		firstRow: 'ct-brandA-in',
		page: () => import('./accounts/+page.svelte') as never,
		load: () => import('./accounts/+page') as never
	},
	{
		path: '/tenants',
		heading: 'Tenants',
		op: 'listTenants',
		firstRow: 'orders-service',
		page: () => import('./tenants/+page.svelte') as never,
		load: () => import('./tenants/+page') as never
	},
	{
		path: '/snapshots',
		heading: 'Snapshots',
		op: 'listSnapshots',
		firstRow: '413',
		page: () => import('./snapshots/+page.svelte') as never,
		load: () => import('./snapshots/+page') as never
	},
	{
		path: '/change-log',
		heading: 'Change log',
		op: 'listChangeLog',
		firstRow: 'chg_01J9Z4B2C3D4E5F6G7H8J9K0N5',
		page: () => import('./change-log/+page.svelte') as never,
		load: () => import('./change-log/+page') as never
	}
];

async function renderSection(c: SectionCase) {
	const [{ default: Page }, { load }] = await Promise.all([c.page(), c.load()]);
	// Layout data every (app) page receives (HUB-253).
	const layout = { access: accessFromRoles(['editor']) };
	const data = await load({
		fetch,
		url: new URL(`http://console.test${c.path}`),
		parent: async () => layout
	});
	return render(Page, { props: { data: { ...layout, ...(data as object) } as never } });
}

beforeEach(() => invalidateAll.mockReset());

describe.each(cases)('$path', (c) => {
	it(`loads ${c.op} and lists the contract example`, async () => {
		await renderSection(c);
		expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(c.heading);
		const table = await screen.findByRole('table', { busy: false });
		const rows = within(table).getAllByRole('row');
		const example = contractMocks[c.op].example as { items: unknown[] };
		expect(rows).toHaveLength(example.items.length + 1);
		expect(within(table).getAllByRole('rowheader')[0]).toHaveTextContent(c.firstRow);
		// Row count, or the pager on screens that page (HUB-256).
		expect(screen.getByText(/rows? shown|^Page 1:/)).toBeInTheDocument();
	});

	it('shows a loading state while the call is pending', async () => {
		server.use(mockPending(c.op));
		await renderSection(c);
		expect(screen.getByRole('table')).toHaveAttribute('aria-busy', 'true');
		// Some sections load a second call beside the list (snapshots: the latest one, HUB-273).
		expect(screen.getAllByText(/^Loading /).length).toBeGreaterThanOrEqual(1);
	});

	it('shows an empty state', async () => {
		server.use(mockOperation(c.op, { items: [], next_cursor: null } as never));
		await renderSection(c);
		await waitFor(() => expect(screen.queryByRole('table')).toBeNull());
		const empty = screen
			.getAllByRole('status')
			.filter((s) => /^(No |The dead-letter queue is empty)/.test(s.textContent?.trim() ?? ''));
		expect(empty).toHaveLength(1);
	});

	it('shows the error envelope with request id and retries', async () => {
		server.use(mockError(c.op, 403));
		await renderSection(c);
		const alert = await screen.findByRole('alert');
		const example = contractMocks[c.op].errors['403']!.error;
		expect(alert).toHaveTextContent('FORBIDDEN');
		expect(alert).toHaveTextContent(example.request_id);
		await userEvent.click(within(alert).getByRole('button', { name: 'Retry' }));
		expect(invalidateAll).toHaveBeenCalledOnce();
	});
});

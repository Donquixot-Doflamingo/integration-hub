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

type LoadFn = (event: { fetch: typeof fetch; url: URL }) => unknown;
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
		path: '/health',
		heading: 'Health',
		op: 'listBreakers',
		firstRow: 'ct-brandA-in',
		page: () => import('./health/+page.svelte') as never,
		load: () => import('./health/+page') as never
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
	const data = await load({ fetch, url: new URL(`http://console.test${c.path}`) });
	// Layout data every (app) page receives (HUB-253).
	const layout = { access: accessFromRoles(['editor']) };
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
		expect(screen.getByText(/^Loading /)).toBeInTheDocument();
	});

	it('shows an empty state', async () => {
		server.use(mockOperation(c.op, { items: [], next_cursor: null } as never));
		await renderSection(c);
		await waitFor(() => expect(screen.queryByRole('table')).toBeNull());
		expect(screen.getByRole('status')).toHaveTextContent(/^(No |The dead-letter queue is empty)/);
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

describe('/health', () => {
	it('sorts open breakers first and labels state with text', async () => {
		const base = (contractMocks.listBreakers.example.items[0] ?? {}) as Record<string, unknown>;
		server.use(
			mockOperation('listBreakers', {
				items: [
					{ ...base, account_id: 'acct-closed', state: 'closed' },
					{ ...base, account_id: 'acct-half', state: 'half_open' },
					{ ...base, account_id: 'acct-open', state: 'open' }
				],
				next_cursor: null
			} as never)
		);
		await renderSection(cases[1]!);
		const table = await screen.findByRole('table', { busy: false });
		const heads = within(table)
			.getAllByRole('rowheader')
			.map((h) => h.textContent);
		expect(heads.map((h) => h?.split(' ')[0])).toEqual(['acct-open', 'acct-half', 'acct-closed']);
		expect(within(table).getByText('Open')).toBeInTheDocument();
		expect(within(table).getByText('Half-open')).toBeInTheDocument();
		expect(within(table).getByText('Closed')).toBeInTheDocument();
	});
});

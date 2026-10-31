import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { invalidate } from '$app/navigation';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mockOperation, mockPending } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import type { Breaker } from '$lib/api/types';
import Page from './+page.svelte';
import { load } from './+page';

// HUB-261: vendor and account health board against MSW.

vi.mock('$app/navigation', () => ({ invalidate: vi.fn(), invalidateAll: vi.fn(), goto: vi.fn() }));

const base = contractMocks.listBreakers.example.items[0]!;
const b = (over: Partial<Breaker>): Breaker => ({ ...base, ...over });

function loadBoard() {
	return load({
		fetch,
		url: new URL('http://console.test/health'),
		depends: vi.fn()
	} as never) as { board: Promise<unknown> };
}

function renderBoard(access: Access = accessFromRoles(['viewer'])) {
	return render(Page, { props: { data: { access, ...loadBoard() } as never } });
}

beforeEach(() => vi.mocked(invalidate).mockReset());
afterEach(() => vi.useRealTimers());

describe('health board', () => {
	it('groups by vendor, puts open breakers first and says so in text', async () => {
		server.use(
			mockOperation('listBreakers', {
				items: [
					b({ vendor_id: 'moengage', account_id: 'mo-in', state: 'closed', parked_count: 0 }),
					b({ account_id: 'ct-half', state: 'half_open', parked_count: 3 }),
					b({ account_id: 'ct-brandA-in', state: 'open' })
				],
				next_cursor: null
			})
		);
		renderBoard();
		const table = await screen.findByRole('table');
		const heads = within(table)
			.getAllByRole('rowheader')
			.map((h) => h.textContent?.trim());
		expect(heads[0]).toMatch(/^clevertap/);
		expect(heads[1]).toMatch(/^ct-brandA-in\s+upload@1/);
		expect(heads[2]).toMatch(/^ct-half/);
		expect(heads[3]).toMatch(/^moengage/);
		const open = within(table).getAllByRole('row')[2]!;
		expect(open).toHaveTextContent('Open');
		expect(open).toHaveTextContent('82%');
		expect(open).toHaveTextContent('1,290');
		expect(open).toHaveTextContent('61 s');
		expect(open).toHaveTextContent('Active');
		expect(open).toHaveTextContent(/probe due|retry in/);
		expect(screen.getByText(/1 open/).closest('[role="status"]')).toHaveTextContent(
			'1 open, 1 half-open, 1 closed. 1,293 requests parked.'
		);
	});

	it('shows forced breakers with their hold', async () => {
		server.use(
			mockOperation('listBreakers', {
				items: [contractMocks.forceOpenBreaker.example.breaker],
				next_cursor: null
			})
		);
		renderBoard();
		const table = await screen.findByRole('table');
		expect(table).toHaveTextContent('Forced open until 10:30 UTC');
	});

	it('still shows breakers when accounts cannot be listed', async () => {
		server.use(mockError('listAccounts', 500));
		renderBoard();
		const table = await screen.findByRole('table');
		expect(table).toHaveTextContent('Unknown');
		expect(screen.getByRole('note')).toHaveTextContent('Account lifecycle states are missing');
		expect(screen.getByRole('note')).toHaveTextContent('INTERNAL_ERROR');
	});

	it('shows a loading state', () => {
		server.use(mockPending('listBreakers'));
		renderBoard();
		expect(screen.getByRole('status')).toHaveTextContent('Loading the health board...');
	});

	it('shows an empty state', async () => {
		server.use(mockOperation('listBreakers', { items: [], next_cursor: null }));
		renderBoard();
		expect(await screen.findByText('No breakers reported')).toBeInTheDocument();
	});

	it('shows the error with its request id', async () => {
		server.use(mockError('listBreakers', 403));
		renderBoard();
		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent('FORBIDDEN');
		expect(alert).toHaveTextContent(contractMocks.listBreakers.errors['403']!.error.request_id);
	});

	it('keeps the last board, marked stale, when a refresh fails', async () => {
		const view = renderBoard();
		await screen.findByRole('table');
		server.use(mockError('listBreakers', 500));
		await view.rerender({ data: { access: accessFromRoles(['viewer']), ...loadBoard() } as never });
		const stale = await screen.findByText(/Stale: the last refresh failed/);
		expect(stale).toHaveTextContent('INTERNAL_ERROR');
		expect(screen.getByRole('table')).toHaveTextContent('ct-brandA-in');
	});

	it('tells tenant viewers why the board is not for them', () => {
		renderBoard(accessFromRoles(['tenant_viewer']));
		expect(screen.getByRole('note')).toHaveTextContent('tenant viewers cannot see');
		expect(screen.queryByRole('table')).toBeNull();
	});
});

describe('auto-refresh', () => {
	beforeEach(() =>
		vi.useFakeTimers({ shouldAdvanceTime: true, toFake: ['setInterval', 'clearInterval', 'Date'] })
	);

	it('refreshes every 5 s by default, at the chosen interval, and can be paused', async () => {
		const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
		renderBoard();
		await screen.findByRole('table');
		const calls = () => vi.mocked(invalidate).mock.calls.length;
		vi.advanceTimersByTime(5000);
		expect(calls()).toBe(1);
		expect(invalidate).toHaveBeenCalledWith('hub:health');

		await user.selectOptions(screen.getByLabelText('Refresh every'), '30 s');
		vi.advanceTimersByTime(10_000);
		expect(calls()).toBe(1);
		vi.advanceTimersByTime(20_000);
		expect(calls()).toBe(2);

		await user.click(screen.getByRole('button', { name: 'Pause auto-refresh' }));
		vi.advanceTimersByTime(120_000);
		expect(calls()).toBe(2);
		expect(screen.getByText(/^Paused\. Updated/)).toBeInTheDocument();

		await user.click(screen.getByRole('button', { name: 'Resume auto-refresh' }));
		await user.click(screen.getByRole('button', { name: 'Refresh now' }));
		expect(calls()).toBe(3);
	});

	it('shows how long ago the board was updated', async () => {
		renderBoard();
		await screen.findByRole('table');
		expect(screen.getByText(/Updated just now/)).toBeInTheDocument();
		vi.advanceTimersByTime(4000);
		expect(await screen.findByText(/Updated 4 s ago/)).toBeInTheDocument();
	});
});

import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles } from '$lib/auth/roles';
import Page from './+page.svelte';
import { load } from './+page';

// HUB-273: snapshots list, latest highlighted, diff summary expandable.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const example = contractMocks.listSnapshots.example.items[0]!;
const older = {
	...example,
	version: 412,
	change_id: null,
	summary: 'pause int_evt_brandB',
	blob_url: '/admin/v1/snapshots/412/blob'
};

function renderAt(search = '') {
	const data = load({ fetch, url: new URL(`http://console.test/snapshots${search}`) } as never);
	render(Page, { props: { data: { access: accessFromRoles(['viewer']), ...data } as never } });
	return screen.findByRole('table', { busy: false });
}

beforeEach(() => {
	server.events.removeAllListeners();
	server.use(
		http.get(mswPath('/snapshots'), () =>
			HttpResponse.json({ items: [example, older], next_cursor: 'eyJrIjoiNDEyIn0' })
		)
	);
});

describe('snapshots', () => {
	it('marks the latest snapshot in text and in its own summary', async () => {
		const table = await renderAt();
		const [, latestRow, olderRow] = within(table).getAllByRole('row');
		await vi.waitFor(() => expect(within(latestRow!).getByText('Latest')).toBeInTheDocument());
		expect(within(olderRow!).queryByText('Latest')).toBeNull();
		const latest = screen.getByRole('region', { name: 'Latest snapshot' });
		expect(latest).toHaveTextContent('Snapshot 413');
		expect(latest).toHaveTextContent('publish int_evt_brandA v8');
		expect(latest).toHaveTextContent('2026-10-31 09:30 UTC by priya@example.com');
		expect(latest).toHaveTextContent('sha256:5e88489c3b1a');
	});

	it('shows version, checksum (short, full in the title), author, change link, size and time', async () => {
		const table = await renderAt();
		const row = within(table).getAllByRole('row')[1]!;
		expect(within(row).getByText('sha256:5e88489c3b1a')).toHaveAttribute('title', example.checksum);
		expect(within(row).getByRole('link', { name: example.change_id! })).toHaveAttribute(
			'href',
			`/change-log/${example.change_id}`
		);
		expect(row).toHaveTextContent('47.1 KiB');
		expect(within(table).getAllByRole('row')[2]).toHaveTextContent('-');
		expect(screen.getByRole('link', { name: /Next/ })).toHaveAttribute(
			'href',
			'/snapshots?cursor=eyJrIjoiNDEyIn0&prev='
		);
	});

	it('expands the diff summary on demand (getSnapshot) and collapses it', async () => {
		const reads: string[] = [];
		server.events.on('request:start', ({ request }) => {
			if (/\/snapshots\/\d+$/.test(new URL(request.url).pathname)) reads.push(request.url);
		});
		const user = userEvent.setup();
		const table = await renderAt();
		expect(reads).toEqual([]);
		const toggle = within(table).getByRole('button', { name: 'Show changes in 413' });
		expect(toggle).toHaveAttribute('aria-expanded', 'false');
		await user.click(toggle);
		expect(toggle).toHaveAttribute('aria-expanded', 'true');
		const panel = document.getElementById(toggle.getAttribute('aria-controls')!)!;
		expect(await within(panel).findByText(/Versus snapshot 412/)).toBeInTheDocument();
		expect(panel).toHaveTextContent('Updated integration int_evt_brandA');
		expect(within(panel).getByRole('link', { name: 'int_evt_brandA' })).toHaveAttribute(
			'href',
			'/integrations/int_evt_brandA'
		);
		await user.click(within(table).getByRole('button', { name: 'Hide changes in 413' }));
		expect(panel).not.toBeVisible();
		await user.click(within(table).getByRole('button', { name: 'Show changes in 413' }));
		expect(reads).toHaveLength(1);
	});

	it('shows a failed diff summary with its request id inside the row', async () => {
		server.use(mockError('getSnapshot', 404));
		const user = userEvent.setup();
		const table = await renderAt();
		await user.click(within(table).getByRole('button', { name: 'Show changes in 413' }));
		const alert = await within(table).findByRole('alert');
		expect(alert).toHaveTextContent('NOT_FOUND');
		expect(alert).toHaveTextContent(contractMocks.getSnapshot.errors['404']!.error.request_id);
	});

	it('still lists snapshots when the latest cannot be read', async () => {
		server.use(mockError('getLatestSnapshot', 500));
		const table = await renderAt();
		expect(within(table).getAllByRole('row')).toHaveLength(3);
		expect(within(table).queryByText('Latest')).toBeNull();
		expect(
			await within(screen.getByRole('region', { name: 'Latest snapshot' })).findByRole('alert')
		).toHaveTextContent('INTERNAL_ERROR');
	});

	it('sends the cursor from the URL', async () => {
		const seen: URLSearchParams[] = [];
		server.events.on('request:start', ({ request }) => {
			const u = new URL(request.url);
			if (u.pathname.endsWith('/admin/v1/snapshots')) seen.push(u.searchParams);
		});
		await renderAt('?cursor=abc&limit=25');
		expect(Object.fromEntries(seen[0]!)).toEqual({ cursor: 'abc', limit: '25' });
	});
});

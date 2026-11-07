import { isRedirect } from '@sveltejs/kit';
import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mockOperation } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles } from '$lib/auth/roles';
import Page from './+page.svelte';
import { load } from './+page';

// HUB-258: version diff against MSW (contract example: v7 -> v8).

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const event = (search: string) =>
	({
		fetch,
		url: new URL(`http://console.test/integrations/int_evt_brandA/diff${search}`),
		params: { integration_id: 'int_evt_brandA' }
	}) as never;

async function renderDiff(search: string) {
	const data = (await load(event(search))) as object;
	return render(Page, {
		props: { data: { access: accessFromRoles(['viewer']), ...data } as never }
	});
}

describe('diff page', () => {
	it('redirects to a default pair (newest vs active) when the URL names none', async () => {
		try {
			await load(event('?view=paths'));
			expect.unreachable();
		} catch (e) {
			expect(isRedirect(e)).toBe(true);
			expect(e).toMatchObject({
				status: 307,
				location: '/integrations/int_evt_brandA/diff?from=7&to=8&view=paths'
			});
		}
	});

	it('shows the side-by-side diff with removed and added lines marked in text', async () => {
		await renderDiff('?from=7&to=8');
		expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(
			'Compare versions of int_evt_brandA'
		);
		const region = await screen.findByRole('region', { name: 'Side-by-side diff' });
		const table = within(region).getByRole('table');
		expect(
			within(table)
				.getAllByRole('columnheader')
				.map((h) => h.textContent)
		).toEqual(['v7', 'v8']);
		expect(table).toHaveTextContent('Removed: weight: 100');
		expect(table).toHaveTextContent('Added: weight: 90');
		expect(table).toHaveTextContent('Added: account_id: ct-brandA-in-v2');
		expect(
			screen.getByText(
				'3 paths differ (2 added, 1 changed): targets[0].weight, targets[1], operations.default.request_schema.body.properties.context.properties.channel'
			)
		).toBeInTheDocument();
		expect(
			within(screen.getByRole('navigation', { name: 'Diff view' })).getByRole('link', {
				name: 'Side by side'
			})
		).toHaveAttribute('aria-current', 'page');
	});

	it('shows the path list with before and after', async () => {
		await renderDiff('?from=7&to=8&view=paths');
		const region = await screen.findByRole('region', { name: 'Changed paths' });
		const rows = within(region).getAllByRole('row').slice(1);
		expect(rows[0]).toHaveTextContent('targets[0].weight');
		expect(rows[0]).toHaveTextContent('/targets/0/weight');
		expect(rows[0]).toHaveTextContent('Changed');
		expect(
			within(rows[0]!)
				.getAllByRole('cell')
				.map((c) => c.textContent?.trim())
		).toEqual(['~ Changed', '100', '90']);
		expect(rows[1]).toHaveTextContent('Added');
		expect(rows[1]).toHaveTextContent('Not present');
		expect(rows[1]).toHaveTextContent('"account_id": "ct-brandA-in-v2"');
	});

	it('marks removed paths', async () => {
		server.use(
			mockOperation('diffIntegrationVersions', {
				...contractMocks.diffIntegrationVersions.example,
				changes: [
					{
						op: 'remove',
						path: '/webhooks/0',
						before: { event: 'delivery.status', endpoint_id: 'reg_orders_cb' },
						after: null
					}
				]
			})
		);
		await renderDiff('?from=7&to=8&view=paths');
		const region = await screen.findByRole('region', { name: 'Changed paths' });
		expect(region).toHaveTextContent('webhooks[0]');
		expect(region).toHaveTextContent('Removed');
		expect(screen.getByText('1 path differ (1 removed): webhooks[0]')).toBeInTheDocument();
	});

	it('version pickers show the pair and submit a GET form', async () => {
		await renderDiff('?from=7&to=8');
		const form = await screen.findByRole('form', { name: 'Choose versions' });
		expect(form).toHaveAttribute('method', 'GET');
		expect(form).toHaveAttribute('action', '/integrations/int_evt_brandA/diff');
		expect(within(form).getByLabelText('From (old)')).toHaveValue('7');
		expect(within(form).getByLabelText('To (new)')).toHaveValue('8');
		expect(
			within(within(form).getByLabelText('To (new)'))
				.getAllByRole('option')
				.map((o) => o.textContent)
		).toEqual(['v8 (draft)', 'v7 (active)']);
		expect(within(form).getByRole('link', { name: 'Swap sides' })).toHaveAttribute(
			'href',
			'/integrations/int_evt_brandA/diff?from=8&to=7'
		);
	});

	it('does not call the API for the same version on both sides', async () => {
		const calls = vi.fn();
		server.events.on('request:start', ({ request }) => {
			if (request.url.includes('/diff')) calls();
		});
		await renderDiff('?from=7&to=7');
		expect(await screen.findByText('Choose two different versions')).toBeInTheDocument();
		expect(calls).not.toHaveBeenCalled();
		server.events.removeAllListeners();
	});

	it('shows a diff error with the request id', async () => {
		server.use(mockError('diffIntegrationVersions', 404));
		await renderDiff('?from=7&to=99');
		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent('Could not load the diff v7 to v99');
		expect(alert).toHaveTextContent(
			contractMocks.diffIntegrationVersions.errors['404']!.error.request_id
		);
	});

	it('expands folded unchanged lines by keyboard', async () => {
		const context = Array.from({ length: 20 }, (_, i) => ` line ${i}`).join('\n');
		server.use(
			mockOperation('diffIntegrationVersions', {
				...contractMocks.diffIntegrationVersions.example,
				unified: `--- v7\n+++ v8\n@@ delivery @@\n${context}\n-  wait_ms: 3000\n+  wait_ms: 5000\n`
			})
		);
		await renderDiff('?from=7&to=8');
		const show = await screen.findByRole('button', { name: 'Show 14 unchanged lines' });
		expect(screen.queryByText('line 10')).toBeNull();
		show.focus();
		await userEvent.keyboard('{Enter}');
		expect(screen.getAllByText('line 10')).toHaveLength(2);
	});
});

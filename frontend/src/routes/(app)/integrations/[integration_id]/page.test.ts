import { render, screen, within } from '@testing-library/svelte';
import { http, HttpResponse } from 'msw';
import { describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mockOperation, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import type { Role } from '$lib/api/types';
import Page from './+page.svelte';
import { load } from './+page';
import VersionPage from './versions/[version]/+page.svelte';
import { load as versionLoad } from './versions/[version]/+page';

// HUB-257: integration detail and version history against MSW (contract examples).

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const integration = contractMocks.getIntegration.example;
const v7 = contractMocks.listIntegrationVersions.example.items[1]!;

async function renderDetail(id = 'int_evt_brandA', access: Access = accessFromRoles(['editor'])) {
	const data = load({
		fetch,
		url: new URL(`http://console.test/integrations/${id}`),
		params: { integration_id: id }
	} as never) as object;
	return render(Page, { props: { data: { access, ...data } as never } });
}

describe('integration detail', () => {
	it('shows the header and the active version spec', async () => {
		await renderDetail();
		expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('int_evt_brandA');
		const summary = await screen.findByRole('region', { name: 'Summary' });
		expect(summary).toHaveTextContent('Live');
		expect(summary).toHaveTextContent('Brand A product analytics events');
		expect(summary).toHaveTextContent('orders-service');
		expect(summary).toHaveTextContent(/Interactive\s*No/);
		expect(summary).toHaveTextContent('v8 (draft)');

		expect(screen.getByRole('heading', { name: 'Active version v7' })).toBeInTheDocument();
		const targets = screen.getByRole('region', { name: 'Targets' });
		expect(within(targets).getByRole('rowheader')).toHaveTextContent('clevertap/ct-brandA-in');
		expect(targets).toHaveTextContent('T1@7');
		expect(targets).toHaveTextContent(/Total\s*100%/);
		const delivery = screen.getByRole('region', { name: 'Delivery' });
		expect(delivery).toHaveTextContent('wait');
		expect(delivery).toHaveTextContent('3,000 ms');
		expect(delivery).toHaveTextContent('6 h');
		expect(delivery).toHaveTextContent('Queue if the mode allows it');
		expect(screen.getByRole('region', { name: 'Breaker' })).toHaveTextContent(
			/Open after consecutive timeouts\s*3/
		);
		expect(screen.getByRole('region', { name: 'Webhooks' })).toHaveTextContent(
			'delivery.status to reg_orders_cb'
		);
		expect(screen.getByRole('region', { name: 'Audit' })).toHaveTextContent('Masked payloads');
		expect(screen.getByRole('region', { name: 'Audit' })).toHaveTextContent('30 d');
		expect(screen.getByRole('region', { name: 'Failover' })).toHaveTextContent(
			'No failover targets.'
		);
	});

	it('lists every version newest first, across pages, with the active one marked in text', async () => {
		server.use(
			http.get(mswPath('/integrations/:integration_id/versions'), ({ request }) => {
				const cursor = new URL(request.url).searchParams.get('cursor');
				// Deliberately out of order and split over two pages.
				return HttpResponse.json(
					cursor
						? {
								items: [
									{ ...v7, version: 5, is_active: false, reason: 'First publish', based_on: null }
								],
								next_cursor: null
							}
						: {
								items: [
									{ ...v7, version: 6, is_active: false, reason: 'Retry tuning' },
									{ ...v7 },
									{ ...v7, version: 8, state: 'draft', is_active: false, published_at: null }
								],
								next_cursor: 'p2'
							}
				);
			})
		);
		await renderDetail();
		const table = await screen.findByRole('table', {
			name: /Versions of int_evt_brandA/,
			busy: false
		});
		const rows = within(table).getAllByRole('row').slice(1);
		expect(rows.map((r) => within(r).getByRole('rowheader').textContent?.trim())).toEqual([
			'v8',
			'v7',
			'v6',
			'v5'
		]);
		expect(rows[0]).toHaveTextContent('Draft');
		expect(rows[1]).toHaveTextContent('Active');
		expect(rows[1]).toHaveTextContent('priya@example.com');
		expect(rows[1]).toHaveTextContent('Add breaker override');
		expect(rows[1]).toHaveTextContent('2026-10-29 08:05 UTC');
		expect(rows[2]).toHaveTextContent('Published');
		expect(rows[3]).toHaveTextContent('First publish');
		expect(within(rows[1]!).getByRole('link', { name: 'v7' })).toHaveAttribute(
			'href',
			'/integrations/int_evt_brandA/versions/7'
		);
		expect(screen.getByText('4 versions.')).toBeInTheDocument();

		// HUB-258: any two versions can be compared from the history.
		const form = screen.getByRole('form', { name: 'Compare versions' });
		expect(form).toHaveAttribute('action', '/integrations/int_evt_brandA/diff');
		expect(within(rows[1]!).getByRole('radio', { name: 'Compare from v7' })).toBeChecked();
		expect(within(rows[0]!).getByRole('radio', { name: 'Compare to v8' })).not.toBeChecked();
		// v8 in this fixture is based on v6 (copied from the v7 example).
		expect(within(rows[0]!).getByRole('link', { name: 'vs v6' })).toHaveAttribute(
			'href',
			'/integrations/int_evt_brandA/diff?from=6&to=8'
		);
		expect(
			within(form).getByRole('button', { name: 'Compare selected versions' })
		).toBeInTheDocument();
	});

	it('says when there is no active version', async () => {
		server.use(
			mockOperation('getIntegration', { ...integration, active: null, active_version: null })
		);
		await renderDetail();
		expect(await screen.findByText(/Invocations answer 503/)).toBeInTheDocument();
		expect(screen.getByRole('region', { name: 'Summary' })).toHaveTextContent('No active version');
	});

	it('shows a paused integration with its reason', async () => {
		server.use(
			mockOperation('getIntegration', {
				...integration,
				paused: true,
				paused_reason: 'Vendor outage'
			})
		);
		await renderDetail();
		expect(await screen.findByText('Paused: Vendor outage')).toBeInTheDocument();
	});

	it.each<{ role: Role; readOnly: boolean }>([
		{ role: 'admin', readOnly: false },
		{ role: 'editor', readOnly: false },
		{ role: 'viewer', readOnly: true },
		{ role: 'tenant_viewer', readOnly: true }
	])('$role: read-only note $readOnly', async ({ role, readOnly }) => {
		await renderDetail('int_evt_brandA', accessFromRoles([role]));
		await screen.findByRole('region', { name: 'Summary' });
		expect(!!screen.queryByText(/Read only\. Publishing, rolling back/)).toBe(readOnly);
	});

	it('shows NOT_FOUND with the request id', async () => {
		server.use(mockError('getIntegration', 404), mockError('listIntegrationVersions', 404));
		await renderDetail('int_gone');
		const alerts = await screen.findAllByRole('alert');
		expect(alerts[0]).toHaveTextContent('Could not load integration int_gone');
		expect(alerts[0]).toHaveTextContent('NOT_FOUND');
		expect(alerts[0]).toHaveTextContent(
			contractMocks.getIntegration.errors['404']!.error.request_id
		);
	});
});

describe('integration version page', () => {
	const renderVersion = (version: string) => {
		const data = versionLoad({
			fetch,
			url: new URL(`http://console.test/integrations/int_evt_brandA/versions/${version}`),
			params: { integration_id: 'int_evt_brandA', version }
		} as never) as object;
		return render(VersionPage, {
			props: { data: { access: accessFromRoles(['viewer']), ...data } as never }
		});
	};

	it('shows one version with its spec', async () => {
		renderVersion('7');
		expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('int_evt_brandA v7');
		const summary = await screen.findByRole('region', { name: 'Version summary' });
		expect(summary).toHaveTextContent('Published');
		expect(summary).toHaveTextContent('Add breaker override');
		expect(summary).toHaveTextContent(/Snapshot\s*412/);
		expect(screen.getByRole('region', { name: 'Targets' })).toHaveTextContent(
			'clevertap/ct-brandA-in'
		);
		expect(screen.getByRole('link', { name: 'All versions of int_evt_brandA' })).toHaveAttribute(
			'href',
			'/integrations/int_evt_brandA'
		);
	});

	it('rejects a non-numeric version with a 404', () => {
		expect(() => renderVersion('latest')).toThrow(expect.objectContaining({ status: 404 }));
	});
});

import { render, screen, within } from '@testing-library/svelte';
import { http, HttpResponse } from 'msw';
import { describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles } from '$lib/auth/roles';
import Page from './+page.svelte';
import { load } from './+page';

// HUB-274: one change log entry with its before / after as a diff.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const ID = 'chg_01J9Z4B2C3D4E5F6G7H8J9K0N5';
const entry = contractMocks.getChangeLogEntry.example;

function renderEntry(id = ID) {
	const data = load({
		fetch,
		url: new URL(`http://console.test/change-log/${id}`),
		params: { change_id: id }
	} as never) as object;
	render(Page, { props: { data: { access: accessFromRoles(['viewer']), ...data } as never } });
}

describe('change log entry', () => {
	it('shows who, what, why and the before / after as a diff (contract example)', async () => {
		renderEntry();
		const head = await screen.findByRole('region', { name: 'Summary' });
		expect(head).toHaveTextContent('publishIntegrationVersion on integration int_evt_brandA');
		expect(head).toHaveTextContent('Canary 20% to the new workspace');
		expect(head).toHaveTextContent('priya@example.com (editor)');
		expect(within(head).getByRole('link', { name: 'int_evt_brandA' })).toHaveAttribute(
			'href',
			'/integrations/int_evt_brandA'
		);
		expect(within(head).getByRole('link', { name: 'priya@example.com' })).toHaveAttribute(
			'href',
			'/change-log?actor=priya%40example.com'
		);
		expect(
			within(head).getByRole('link', { name: 'Every change to int_evt_brandA' })
		).toHaveAttribute('href', '/change-log?object_type=integration&object_id=int_evt_brandA');
		const diff = screen.getByRole('table', { name: /Changed fields/ });
		const row = within(diff).getByRole('row', { name: /active_version/ });
		expect(row).toHaveTextContent('Changed');
		expect(
			within(row)
				.getAllByRole('cell')
				.map((c) => c.textContent?.trim())
		).toEqual(['~ Changed', '7', '8']);
		expect(screen.getByText(/1 path differ \(1 changed\): active_version/)).toBeInTheDocument();
	});

	it('a create lists every field as added', async () => {
		server.use(
			http.get(mswPath('/change-log/:change_id'), () =>
				HttpResponse.json({
					...entry,
					action: 'createTenant',
					object_type: 'tenant',
					object_id: 'orders-service',
					before: null,
					after: { tenant_id: 'orders-service', disabled: false }
				})
			)
		);
		renderEntry();
		expect(await screen.findByText(/Created: there was no object before/)).toBeInTheDocument();
		const diff = screen.getByRole('table', { name: /Changed fields/ });
		expect(within(diff).getAllByRole('row', { name: /Added/ })).toHaveLength(2);
		expect(screen.getByRole('link', { name: 'orders-service' })).toHaveAttribute(
			'href',
			'/tenants/orders-service'
		);
	});

	it('shows the error with its request id', async () => {
		server.use(mockError('getChangeLogEntry', 404));
		renderEntry();
		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent('NOT_FOUND');
		expect(alert).toHaveTextContent(
			contractMocks.getChangeLogEntry.errors['404']!.error.request_id
		);
	});

	it('answers 404 for something that is not a change id, without a call', () => {
		expect(() =>
			load({
				fetch,
				url: new URL('http://console.test/change-log/nope'),
				params: { change_id: 'nope' }
			} as never)
		).toThrow(expect.objectContaining({ status: 404 }));
	});
});

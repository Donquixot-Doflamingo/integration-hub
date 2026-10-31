import { render, screen, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mockOperation, mockPending } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import Page from './+page.svelte';
import { load } from './+page';

// HUB-266: DLQ entry detail (reason, attempt history, payloads) against MSW.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const example = contractMocks.getDeadLetter.example;
const id = example.request_id;

function renderEntry(access: Access = accessFromRoles(['viewer']), requestId = id) {
	const data = load({
		fetch,
		url: new URL(`http://console.test/dlq/${requestId}`),
		params: { request_id: requestId }
	} as never) as object;
	return render(Page, { props: { data: { access, ...data } as never } });
}

describe('DLQ entry', () => {
	it('shows the reason and message first', async () => {
		renderEntry();
		const reason = await screen.findByRole('group', { name: 'Reason' });
		expect(reason).toHaveTextContent('UPSTREAM_REJECTED');
		expect(reason).toHaveTextContent('event name must not contain spaces');
		expect(screen.getByText('Waiting for a replay')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Open the request timeline' })).toHaveAttribute(
			'href',
			`/requests/${id}`
		);
		expect(screen.getByText('clevertap / ct-brandA-in')).toBeInTheDocument();
	});

	it('lists the attempt history with version, account and vendor status', async () => {
		renderEntry();
		const history = await screen.findByRole('region', { name: 'Attempts table' });
		const rows = within(history).getAllByRole('row');
		expect(rows).toHaveLength(2);
		expect(rows[1]).toHaveTextContent('v7');
		expect(rows[1]).toHaveTextContent('clevertap/ct-brandA-in');
		expect(rows[1]).toHaveTextContent('upload@1');
		expect(rows[1]).toHaveTextContent('400');
		expect(rows[1]).toHaveTextContent('UPSTREAM_REJECTED');
		expect(rows[1]).toHaveTextContent('Closed');
	});

	it('orders attempts by number whatever the API order', async () => {
		const a = example.attempt_history[0]!;
		server.use(
			mockOperation('getDeadLetter', {
				...example,
				attempts: 2,
				attempt_history: [
					{ ...a, attempt_no: 2, vendor_status: 503 },
					{ ...a, attempt_no: 1, vendor_status: 400 }
				]
			})
		);
		renderEntry();
		const history = await screen.findByRole('region', { name: 'Attempts table' });
		const heads = within(history)
			.getAllByRole('rowheader')
			.map((h) => h.textContent);
		expect(heads).toEqual(['1', '2']);
	});

	it('shows the payloads as returned: credentials stay ***, unmasked secrets are hidden', async () => {
		server.use(
			mockOperation('getDeadLetter', {
				...example,
				last_vendor_request: {
					...example.last_vendor_request,
					headers: { 'X-CleverTap-Passcode': '***', Authorization: 'Bearer leaked' }
				}
			})
		);
		renderEntry();
		await screen.findByRole('group', { name: 'Reason' });
		const request = screen.getByText(/^Show vendor request/).closest('details')!;
		expect(request).toHaveTextContent('"X-CleverTap-Passcode": "***"');
		expect(request).toHaveTextContent('[redacted by console]');
		expect(document.body).not.toHaveTextContent('Bearer leaked');
		expect(screen.getByText(/^Show vendor response/).closest('details')).toHaveTextContent(
			'E_INVALID_EVENT'
		);
		expect(screen.getByText(/^Show canonical request/).closest('details')).toHaveTextContent(
			'hmac:7f1a'
		);
	});

	it('says when nothing was stored', async () => {
		server.use(
			mockOperation('getDeadLetter', {
				...example,
				canonical: null,
				last_vendor_request: null,
				last_vendor_response: null,
				attempt_history: []
			})
		);
		renderEntry();
		expect(await screen.findByText(/No upstream attempt was made/)).toBeInTheDocument();
		expect(screen.getByText(/No canonical request was returned/)).toBeInTheDocument();
		expect(screen.getByText(/No vendor response was returned/)).toBeInTheDocument();
	});

	it('marks a replayed entry', async () => {
		server.use(mockOperation('getDeadLetter', { ...example, replayed_at: '2026-10-31T12:05:00Z' }));
		renderEntry();
		expect(await screen.findByText('Replayed 2026-10-31 12:05 UTC')).toBeInTheDocument();
	});

	it('hides vendor, account and the vendor exchange from tenant viewers', async () => {
		renderEntry(accessFromRoles(['tenant_viewer'], ['orders-service']));
		await screen.findByRole('group', { name: 'Reason' });
		expect(screen.queryByText(/clevertap/)).toBeNull();
		expect(screen.queryByText(/^Show vendor request/)).toBeNull();
		expect(screen.getByRole('note')).toHaveTextContent('tenant viewers cannot see');
		const history = screen.getByRole('region', { name: 'Attempts table' });
		expect(within(history).queryByRole('columnheader', { name: 'Account' })).toBeNull();
	});

	it('shows loading, then a 404 with its request id', async () => {
		server.use(mockPending('getDeadLetter'));
		const view = renderEntry();
		expect(screen.getByText(/^Loading dead letter/)).toBeInTheDocument();
		view.unmount();
		server.use(mockError('getDeadLetter', 404));
		renderEntry();
		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent('NOT_FOUND');
		expect(alert).toHaveTextContent(contractMocks.getDeadLetter.errors['404']!.error.request_id);
	});

	it('answers 404 for something that is not a request id', () => {
		expect(() => renderEntry(undefined, 'not-an-id')).toThrow();
	});
});

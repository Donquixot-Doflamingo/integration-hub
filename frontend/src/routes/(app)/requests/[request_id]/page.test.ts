import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mockOperation } from '$lib/api/mocks/handlers';
import { presets } from '$lib/api/mocks/scenarios';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import Page from './+page.svelte';
import { load } from './+page';

// HUB-264: request timeline against MSW.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const ID = 'req_01J9Z3K8X2V7Q4M6N8P0R2S4T6';
const timelineExample = contractMocks.getRequestTimeline.example;

function renderRequest(id = ID, access: Access = accessFromRoles(['viewer'])) {
	const data = load({
		fetch,
		url: new URL(`http://console.test/requests/${id}`),
		params: { request_id: id }
	} as never) as object;
	render(Page, { props: { data: { access, ...data } as never } });
}

describe('request timeline', () => {
	it('summarises the request', async () => {
		renderRequest();
		const head = await screen.findByRole('region', { name: 'Request summary' });
		expect(head).toHaveTextContent('Succeeded');
		expect(head).toHaveTextContent('orders-service');
		expect(head).toHaveTextContent('int_evt_brandA v7');
		expect(head).toHaveTextContent('clevertap / ct-brandA-in');
		expect(head).toHaveTextContent('order-8821-created');
		expect(head).toHaveTextContent('Masked payloads');
		expect(head).toHaveTextContent(/Hub overhead\s*3 ms/);
		expect(head).toHaveTextContent(/Queue wait\s*1,625 ms/);
		expect(within(head).getByRole('link', { name: 'int_evt_brandA' })).toHaveAttribute(
			'href',
			'/integrations/int_evt_brandA'
		);
	});

	it('lists the events in seq order with their durations, even when the API order is off', async () => {
		server.use(
			mockOperation('getRequestTimeline', {
				...timelineExample,
				events: [...timelineExample.events].reverse()
			})
		);
		renderRequest();
		const status = await screen.findByText(/5 events in sequence order/);
		const list = status.parentElement!.querySelector('ol')!;
		const items = within(list).getAllByRole('listitem');
		expect(items.map((li) => within(li).getByLabelText(/^Sequence/).textContent)).toEqual([
			'#1',
			'#2',
			'#3',
			'#4',
			'#5'
		]);
		expect(items[0]).toHaveTextContent('Request received');
		expect(items[0]).toHaveTextContent('start');
		expect(items[3]).toHaveTextContent('Vendor responded');
		expect(items[3]).toHaveTextContent('+212 ms');
		expect(items[4]).toHaveTextContent('+1,786 ms');
		expect(items[4]).toHaveTextContent('at 1,999 ms');
		expect(items[0]).toHaveTextContent('214 B');
		expect(items[0]).toHaveTextContent('sha256 3a7bd3e2360a3d29...');
	});

	it('shows masked payloads as the API returned them', async () => {
		const user = userEvent.setup();
		renderRequest();
		const status = await screen.findByText(/5 events in sequence order/);
		const first = within(status.parentElement!.querySelector('ol')!).getAllByRole('listitem')[0]!;
		await user.click(within(first).getByText(/Show payload/));
		const json = within(first).getByLabelText('payload as JSON');
		expect(json).toHaveTextContent('"authorization": "sha256:1c9e"');
		expect(json).toHaveTextContent('"id": "hmac:7f1a"');
	});

	it('never renders an unmasked secret', async () => {
		server.use(
			mockOperation('getRequestTimeline', {
				...timelineExample,
				events: [
					{
						...timelineExample.events[0]!,
						payload: { headers: { authorization: 'Bearer live-secret-123' } }
					}
				]
			})
		);
		renderRequest();
		await screen.findByText(/1 event in sequence order/);
		expect(document.body.innerHTML).not.toContain('live-secret-123');
		expect(document.body.innerHTML).toContain('[redacted by console]');
	});

	it('sealed payloads show a locked placeholder and no content', async () => {
		server.use(...presets['sealed-request']!());
		renderRequest();
		const status = await screen.findByText(/5 events in sequence order/);
		const items = within(status.parentElement!.querySelector('ol')!).getAllByRole('listitem');
		expect(items[0]).toHaveTextContent('Sealed payload. Encrypted at rest, 214 B');
		expect(items[0]).toHaveTextContent('sha256 3a7bd3e2360a3d29...');
		expect(within(items[0]!).queryByText(/Show payload/)).toBeNull();
		expect(items[3]).toHaveTextContent('Sealed payload');
		expect(items[2]).toHaveTextContent('Show payload');
		const head = screen.getByRole('region', { name: 'Request summary' });
		expect(head).toHaveTextContent('Sealed payloads');
		expect(head).toHaveTextContent('Sealed result');
	});

	it('metadata-only events say no body was stored', async () => {
		server.use(
			mockOperation('getRequestTimeline', {
				...timelineExample,
				payload_policy: 'metadata_only',
				events: [{ ...timelineExample.events[0]!, payload: null }]
			})
		);
		renderRequest();
		expect(await screen.findByText(/Metadata only: the policy stores no body/)).toBeInTheDocument();
	});

	it('marks truncated payloads and where the rest is', async () => {
		server.use(
			mockOperation('getRequestTimeline', {
				...timelineExample,
				events: [{ ...timelineExample.events[3]!, truncated: true, payload_ref: 's3://audit/x' }]
			})
		);
		renderRequest();
		expect(
			await screen.findByText(
				/truncated at 64 KB; the rest is in object storage \(s3:\/\/audit\/x\)/
			)
		).toBeInTheDocument();
	});

	it('attempt rows show the integration version and the account', async () => {
		renderRequest();
		const table = await screen.findByRole('table', { name: /Upstream attempts/ });
		const row = within(table).getAllByRole('row')[1]!;
		expect(row).toHaveTextContent('v7');
		expect(row).toHaveTextContent('clevertap/ct-brandA-in');
		expect(row).toHaveTextContent('upload@1');
		expect(row).toHaveTextContent('211 ms');
		expect(row).toHaveTextContent('200');
		expect(row).toHaveTextContent('Closed');
	});

	it('tenant viewers see versions but no vendor or account', async () => {
		renderRequest(ID, accessFromRoles(['tenant_viewer'], ['orders-service']));
		const table = await screen.findByRole('table', { name: /Upstream attempts/ });
		expect(within(table).queryByRole('columnheader', { name: 'Account' })).toBeNull();
		expect(table).toHaveTextContent('v7');
		expect(table).not.toHaveTextContent('ct-brandA-in');
		expect(screen.getByRole('region', { name: 'Request summary' })).not.toHaveTextContent(
			'Vendor / account'
		);
	});

	it('errors show the code and request id', async () => {
		server.use(
			mockError('getRequest', 404),
			mockError('getRequestTimeline', 404),
			mockError('listRequestAttempts', 404)
		);
		renderRequest();
		const alerts = await screen.findAllByRole('alert');
		expect(alerts[0]).toHaveTextContent(`Could not load request ${ID}`);
		expect(alerts[0]).toHaveTextContent('NOT_FOUND');
		expect(alerts[0]).toHaveTextContent(contractMocks.getRequest.errors['404']!.error.request_id);
	});

	it('rejects something that is not a request id', () => {
		expect(() => renderRequest('not-an-id')).toThrow(expect.objectContaining({ status: 404 }));
	});
});

import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import { takeFlash } from '$lib/stores/flash.svelte';
import { useActions } from '$lib/testing/forms';
import Page from './[vendor_id]/endpoints/new/+page.svelte';
import { load } from './[vendor_id]/endpoints/new/+page';
import { actions } from './[vendor_id]/endpoints/new/+page.server';

// HUB-436 (ADR-0023): an endpoint version declares the request it accepts (path, query, headers,
// body), built with the schema editor, and optionally documents its response.

const goto = vi.fn();
vi.mock('$app/navigation', () => ({
	goto: (...a: unknown[]) => goto(...a),
	invalidateAll: vi.fn()
}));

const admin = accessFromRoles(['admin']);
const ID = 'clevertap';

async function renderNew(search = '', a: Access = admin) {
	const data = await load({
		fetch,
		url: new URL(`http://console.test/vendors/${ID}/endpoints/new${search}`),
		params: { vendor_id: ID },
		parent: async () => ({ access: a })
	} as never);
	render(Page, { props: { data: { access: a, ...(data as object) } as never } });
}

function capturePost() {
	const bodies: Record<string, unknown>[] = [];
	server.use(
		http.post(mswPath('/vendors/:vendor_id/endpoints'), async ({ request }) => {
			bodies.push((await request.json()) as never);
			return HttpResponse.json(contractMocks.createVendorEndpoint.example, { status: 201 });
		})
	);
	return bodies;
}

beforeEach(() => {
	goto.mockReset();
	takeFlash();
	useActions(actions, { params: { vendor_id: ID } });
});

describe('add an endpoint', () => {
	it('GET / with a required query parameter, built from a sample; the rest is not sent', async () => {
		const bodies = capturePost();
		await renderNew();
		const user = userEvent.setup();
		await user.type(await screen.findByLabelText(/^Name \(required\)/), 'estimate');
		await user.selectOptions(screen.getByLabelText('Method'), 'GET');
		await user.type(screen.getByLabelText(/^Path/), '/');
		await user.click(screen.getByRole('checkbox', { name: /^Idempotent/ }));
		const query = screen.getByRole('group', { name: /^Query parameters/ });
		await user.click(within(query).getByRole('button', { name: 'Paste a sample' }));
		await fireEvent.input(within(query).getByLabelText('Sample query parameters (JSON)'), {
			target: { value: '{"name": "Michael"}' }
		});
		await user.click(within(query).getByRole('button', { name: 'Generate fields' }));
		expect(within(query).getByRole('checkbox', { name: 'name required' })).toBeChecked();
		expect(screen.getByRole('group', { name: /^Body/ })).toHaveTextContent(
			'Nothing declared: not sent.'
		);
		await user.type(screen.getByLabelText(/^Reason/), 'Age estimate endpoint');
		await user.click(screen.getByRole('button', { name: 'Add endpoint' }));
		await waitFor(() => expect(bodies).toHaveLength(1));
		expect(bodies[0]).toMatchObject({
			name: 'estimate',
			method: 'GET',
			path: '/',
			idempotent: true,
			request_schema: {
				query: { type: 'object', required: ['name'], properties: { name: { type: 'string' } } }
			},
			reason: 'Age estimate endpoint'
		});
		expect(Object.keys(bodies[0]!.request_schema as object)).toEqual(['query']);
		expect('response_schema' in bodies[0]!).toBe(false);
		await waitFor(() => expect(goto).toHaveBeenCalledWith('/vendors/clevertap/endpoints/upload@1'));
		expect(takeFlash()?.changeId).toBe(contractMocks.createVendorEndpoint.example.change_id);
	});

	it('path parameters must be declared; one click declares them from the path', async () => {
		const bodies = capturePost();
		await renderNew();
		const user = userEvent.setup();
		await user.type(await screen.findByLabelText(/^Name \(required\)/), 'user');
		await user.type(screen.getByLabelText(/^Path/), '/users/{{id}');
		await user.type(screen.getByLabelText(/^Reason/), 'User lookup endpoint');
		await user.click(screen.getByRole('button', { name: 'Add endpoint' }));
		const path = screen.getByRole('group', { name: /^Path parameters/ });
		expect(await within(path).findByText(/Declare the path parameter \{id\}/)).toBeInTheDocument();
		expect(bodies).toHaveLength(0);
		await user.click(screen.getByRole('button', { name: 'Declare {id} from the path' }));
		expect(within(path).getByRole('checkbox', { name: 'id required' })).toBeChecked();
		await user.click(screen.getByRole('button', { name: 'Add endpoint' }));
		await waitFor(() => expect(bodies).toHaveLength(1));
		expect(bodies[0]!.request_schema).toEqual({
			path: {
				type: 'object',
				required: ['id'],
				properties: { id: { type: 'string', minLength: 1 } }
			}
		});
	});

	it('a new version starts from the latest one: name fixed, request schema kept', async () => {
		const bodies = capturePost();
		await renderNew('?from=upload');
		expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent(
			'New version of clevertap.upload'
		);
		expect(await screen.findByLabelText(/^Name \(required\)/)).toHaveAttribute('readonly');
		const headers = screen.getByRole('group', { name: /^Headers/ });
		expect(within(headers).getByRole('checkbox', { name: 'Content-Type required' })).toBeChecked();
		const user = userEvent.setup();
		await user.type(screen.getByLabelText(/^Reason/), 'Same call, new version');
		await user.click(screen.getByRole('button', { name: 'Create upload@2' }));
		await waitFor(() => expect(bodies).toHaveLength(1));
		expect(bodies[0]!.request_schema).toEqual(
			contractMocks.getVendor.example.endpoints[0]!.request_schema
		);
	});

	it('an API pointer into the request schema lands on its part', async () => {
		server.use(
			mockError('createVendorEndpoint', 400, {
				details: [
					{
						pointer: '/request_schema/body/properties/d',
						keyword: 'schema',
						message: 'does not compile: items must be an object'
					}
				]
			})
		);
		await renderNew('?from=upload');
		const user = userEvent.setup();
		await user.type(await screen.findByLabelText(/^Reason/), 'Same call, new version');
		await user.click(screen.getByRole('button', { name: 'Create upload@2' }));
		const body = screen.getByRole('group', { name: /^Body/ });
		expect(await within(body).findByText(/does not compile/)).toBeInTheDocument();
	});

	it('editors get a read-only note', async () => {
		await renderNew('', accessFromRoles(['editor']));
		expect(screen.getByRole('note')).toHaveTextContent('Adding endpoints needs the Admin role');
	});
});

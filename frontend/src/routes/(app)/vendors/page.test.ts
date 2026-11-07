import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import type { Role } from '$lib/api/types';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import { takeFlash } from '$lib/stores/flash.svelte';
import { useActions } from '$lib/testing/forms';
import ListPage from './+page.svelte';
import { load as listLoad } from './+page';
import NewPage from './new/+page.svelte';
import { actions as newActions } from './new/+page.server';
import DetailPage from './[vendor_id]/+page.svelte';
import { load as detailLoad } from './[vendor_id]/+page';
import EditPage from './[vendor_id]/edit/+page.svelte';
import { load as editLoad } from './[vendor_id]/edit/+page';
import { actions as editActions } from './[vendor_id]/edit/+page.server';
import EndpointPage from './[vendor_id]/endpoints/[endpoint_ref]/+page.svelte';
import { load as endpointLoad } from './[vendor_id]/endpoints/[endpoint_ref]/+page';

// HUB-290, HUB-436: vendors list, create, detail (endpoints, new version), edit with If-Match, roles.

const goto = vi.fn();
vi.mock('$app/navigation', () => ({
	goto: (...a: unknown[]) => goto(...a),
	invalidateAll: vi.fn()
}));

const REASON = 'Onboard Acme for brand C';
const ID = 'clevertap';
const admin = accessFromRoles(['admin']);
const access = (role: Role): Access =>
	accessFromRoles([role], role === 'tenant_viewer' ? ['orders-service'] : []);

const event = (path: string, params: Record<string, string>, a: Access) =>
	({
		fetch,
		url: new URL(`http://console.test${path}`),
		params,
		parent: async () => ({ access: a })
	}) as never;

async function renderList(a: Access = admin) {
	const data = await listLoad(event('/vendors', {}, a));
	render(ListPage, { props: { data: { access: a, ...(data as object) } as never } });
}
async function renderDetail(a: Access = admin) {
	const data = await detailLoad(event(`/vendors/${ID}`, { vendor_id: ID }, a));
	render(DetailPage, { props: { data: { access: a, ...(data as object) } as never } });
}
async function renderEdit(a: Access = admin) {
	const data = await editLoad(event(`/vendors/${ID}/edit`, { vendor_id: ID }, a));
	render(EditPage, { props: { data: { access: a, ...(data as object) } as never } });
	return screen.findByLabelText(/^Display name/);
}

beforeEach(() => {
	goto.mockReset();
	takeFlash();
});

describe('create vendor', () => {
	beforeEach(() => useActions(newActions));

	async function fill() {
		await userEvent.type(screen.getByLabelText(/^Vendor id/), 'acme');
		await userEvent.type(screen.getByLabelText(/^Display name/), 'Acme');
		await userEvent.type(screen.getByLabelText('Region code'), 'in');
		await userEvent.type(screen.getByLabelText('Base URL'), 'https://in.api.acme.example');
		await userEvent.type(screen.getByLabelText(/^Reason/), REASON);
	}

	it('sends nothing while the form has problems, and says what they are', async () => {
		const posts: unknown[] = [];
		server.use(
			http.post(mswPath('/vendors'), async ({ request }) => {
				posts.push(await request.json());
				return HttpResponse.json(contractMocks.createVendor.example, { status: 201 });
			})
		);
		render(NewPage, { props: { data: { access: admin } as never } });
		await userEvent.click(screen.getByRole('button', { name: 'Create vendor' }));
		const summary = await screen.findByRole('alert');
		expect(summary).toHaveTextContent('Enter a vendor id.');
		expect(summary).toHaveTextContent('Add at least one region');
		expect(summary).toHaveTextContent('Enter a reason');
		expect(screen.getByLabelText(/^Vendor id/)).toHaveAttribute('aria-invalid', 'true');
		expect(posts).toHaveLength(0);
	});

	it('creates the vendor with a reason and opens its page with the change log id', async () => {
		const posts: unknown[] = [];
		server.use(
			http.post(mswPath('/vendors'), async ({ request }) => {
				posts.push(await request.json());
				return HttpResponse.json(contractMocks.createVendor.example, { status: 201 });
			})
		);
		render(NewPage, { props: { data: { access: admin } as never } });
		await fill();
		await userEvent.click(screen.getByRole('button', { name: 'Create vendor' }));
		await waitFor(() => expect(goto).toHaveBeenCalledWith('/vendors/clevertap'));
		expect(posts[0]).toMatchObject({
			vendor_id: 'acme',
			display_name: 'Acme',
			regions: { in: 'https://in.api.acme.example' },
			reason: REASON
		});
		expect(takeFlash()).toMatchObject({
			changeId: contractMocks.createVendor.example.change_id,
			message: expect.stringContaining('Created vendor clevertap')
		});
	});

	it('puts an API validation error on its field and keeps the input', async () => {
		server.use(
			mockError('createVendor', 400, {
				message: 'Invalid request body',
				details: [
					{ pointer: '/regions/in', keyword: 'format', message: 'regions/in must be a URI' }
				]
			})
		);
		render(NewPage, { props: { data: { access: admin } as never } });
		await fill();
		await userEvent.click(screen.getByRole('button', { name: 'Create vendor' }));
		await waitFor(() =>
			expect(screen.getByLabelText('Base URL')).toHaveAttribute('aria-invalid', 'true')
		);
		const onField = screen
			.getAllByText('regions/in must be a URI')
			.find((e) => e.classList.contains('fk-error'));
		expect(onField).toBeDefined();
		expect(screen.getByLabelText('Base URL')).toHaveValue('https://in.api.acme.example');
		expect(screen.getByRole('alert')).toHaveTextContent('VALIDATION_FAILED');
		expect(screen.getByRole('alert')).toHaveTextContent(
			contractMocks.createVendor.errors['400']!.error.request_id
		);
		expect(goto).not.toHaveBeenCalled();
	});

	it('a taken id shows the API message (not a reload prompt)', async () => {
		server.use(mockError('createVendor', 409, { message: 'Conflict: vendor acme exists' }));
		render(NewPage, { props: { data: { access: admin } as never } });
		await fill();
		await userEvent.click(screen.getByRole('button', { name: 'Create vendor' }));
		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent('Conflict: vendor acme exists');
		expect(alert).not.toHaveTextContent('Someone else changed');
	});
});

describe('vendor detail', () => {
	it('shows regions, defaults and endpoint versions with the latest marked', async () => {
		await renderDetail();
		const regions = await screen.findByRole('region', { name: 'Region base URLs' });
		expect(regions).toHaveTextContent('https://in1.api.clevertap.com');
		expect(screen.getByText(/Window \(ms\) 30,000/)).toBeInTheDocument();
		const endpoints = screen.getByRole('region', { name: 'Endpoint versions' });
		expect(within(endpoints).getByRole('row', { name: /upload@1/ })).toHaveTextContent('Latest');
	});

	it('links "Add endpoint" and "New version" to the endpoint page, and lists what each declares', async () => {
		await renderDetail();
		const endpoints = await screen.findByRole('region', { name: 'Endpoint versions' });
		expect(screen.getByRole('link', { name: 'Add endpoint' })).toHaveAttribute(
			'href',
			'/vendors/clevertap/endpoints/new'
		);
		expect(within(endpoints).getByRole('link', { name: /New version of upload/ })).toHaveAttribute(
			'href',
			'/vendors/clevertap/endpoints/new?from=upload'
		);
		expect(within(endpoints).getByRole('row', { name: /upload@1/ })).toHaveTextContent(
			'headers (1), body (1)'
		);
	});

	it('the endpoint page offers a new version only on the latest version', async () => {
		const data = await endpointLoad(
			event(`/vendors/${ID}/endpoints/upload@1`, { vendor_id: ID, endpoint_ref: 'upload@1' }, admin)
		);
		render(EndpointPage, { props: { data: { access: admin, ...(data as object) } as never } });
		expect(await screen.findByRole('link', { name: 'New version (upload@2)' })).toHaveAttribute(
			'href',
			'/vendors/clevertap/endpoints/new?from=upload'
		);
		expect(screen.getByText('clevertap.upload@1', { selector: 'code' })).toBeInTheDocument();
		// The request it accepts, part by part.
		expect(screen.getByRole('region', { name: 'Headers of upload@1' })).toHaveTextContent(
			/Content-Type\s*any\s*Required/
		);
		expect(screen.getByRole('region', { name: 'Body of upload@1' })).toHaveTextContent('d');
		expect(
			screen.getByRole('heading', { name: 'Query parameters' }).nextElementSibling
		).toHaveTextContent('Not sent.');
	});
});

describe('edit vendor', () => {
	beforeEach(() => useActions(editActions, { params: { vendor_id: ID } }));

	function capturePut(answer: Response | null = null) {
		const seen: { body: Record<string, unknown>; ifMatch: string | null }[] = [];
		server.use(
			http.put(mswPath('/vendors/:vendor_id'), async ({ request }) => {
				seen.push({
					body: (await request.json()) as never,
					ifMatch: request.headers.get('If-Match')
				});
				return answer ?? HttpResponse.json(contractMocks.updateVendor.example);
			})
		);
		return seen;
	}

	it('sends the whole definition with the loaded ETag as If-Match', async () => {
		const seen = capturePut();
		const name = await renderEdit();
		expect(name).toHaveValue('CleverTap');
		await userEvent.click(screen.getByRole('button', { name: 'Add region' }));
		await userEvent.type(screen.getAllByLabelText('Region code')[2]!, 'sg');
		await userEvent.type(screen.getAllByLabelText('Base URL')[2]!, 'https://sg1.api.clevertap.com');
		await userEvent.type(screen.getByLabelText(/^Reason/), 'Add SG region for brand C');
		await userEvent.click(screen.getByRole('button', { name: 'Save vendor' }));
		await waitFor(() => expect(goto).toHaveBeenCalled());
		expect(seen[0]!.ifMatch).toBe('"3"');
		expect(seen[0]!.body).toMatchObject({
			display_name: 'CleverTap',
			regions: {
				in: 'https://in1.api.clevertap.com',
				us: 'https://us1.api.clevertap.com',
				sg: 'https://sg1.api.clevertap.com'
			},
			reason: 'Add SG region for brand C'
		});
		expect(takeFlash()?.message).toMatch(/Saved vendor clevertap: revision 4/);
	});

	it('refuses to save an unchanged form', async () => {
		const seen = capturePut();
		await renderEdit();
		await userEvent.type(screen.getByLabelText(/^Reason/), 'No change at all here');
		await userEvent.click(screen.getByRole('button', { name: 'Save vendor' }));
		expect(await screen.findByRole('alert')).toHaveTextContent('Nothing changed yet');
		expect(seen).toHaveLength(0);
	});

	it('412: someone else changed it; offers a reload and keeps the edits', async () => {
		server.use(mockError('updateVendor', 412));
		const name = await renderEdit();
		await userEvent.clear(name);
		await userEvent.type(name, 'CleverTap India');
		await userEvent.type(screen.getByLabelText(/^Reason/), 'Rename for the India team');
		await userEvent.click(screen.getByRole('button', { name: 'Save vendor' }));
		expect(await screen.findByText(/Someone else changed clevertap/)).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Reload' })).toBeInTheDocument();
		expect(name).toHaveValue('CleverTap India');
	});
});

describe('roles (x-required-role: createVendor, updateVendor, createVendorEndpoint = admin)', () => {
	it.each<Role>(['editor', 'viewer'])(
		'%s reads vendors but sees no change controls',
		async (role) => {
			await renderList(access(role));
			await screen.findByRole('table', { busy: false });
			expect(screen.queryByRole('link', { name: 'Create vendor' })).toBeNull();
			await renderDetail(access(role));
			await screen.findByRole('region', { name: 'Endpoint versions' });
			expect(screen.queryByRole('link', { name: 'Edit vendor' })).toBeNull();
			expect(screen.queryByRole('link', { name: 'Add endpoint' })).toBeNull();
			expect(screen.queryByRole('link', { name: /New version/ })).toBeNull();
			expect(screen.getByRole('note')).toHaveTextContent('needs the Admin role');
		}
	);

	it('admin sees every control', async () => {
		await renderList();
		expect(await screen.findByRole('link', { name: 'Create vendor' })).toHaveAttribute(
			'href',
			'/vendors/new'
		);
		await renderDetail();
		expect(await screen.findByRole('link', { name: 'Edit vendor' })).toHaveAttribute(
			'href',
			'/vendors/clevertap/edit'
		);
		expect(screen.getByRole('link', { name: 'Add endpoint' })).toBeInTheDocument();
	});

	it('tenant viewers get a note and no vendor call at all', async () => {
		const calls: string[] = [];
		server.events.on('request:start', ({ request }) => calls.push(request.url));
		await renderList(access('tenant_viewer'));
		await renderDetail(access('tenant_viewer'));
		expect(screen.getAllByRole('note')[0]).toHaveTextContent('tenant viewers cannot see them');
		expect(calls.filter((u) => u.includes('/vendors'))).toEqual([]);
		server.events.removeAllListeners();
	});

	it('the create and edit pages are read-only notes without the role', async () => {
		render(NewPage, { props: { data: { access: access('editor') } as never } });
		expect(screen.getByRole('note')).toHaveTextContent('Creating vendors needs the Admin role');
		expect(screen.queryByRole('button', { name: 'Create vendor' })).toBeNull();
	});
});

import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mockOperation, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import type { Role } from '$lib/api/types';
import { accessFromRoles, hasRole, type Access } from '$lib/auth/roles';
import { takeFlash } from '$lib/stores/flash.svelte';
import { runAction, useActions } from '$lib/testing/forms';
import { captureConsole, NOWHERE, secretTraces } from '$lib/testing/secrets';
import ListPage from './+page.svelte';
import { load as listLoad } from './+page';
import NewPage from './new/+page.svelte';
import { load as newLoad } from './new/+page';
import { actions as newActions } from './new/+page.server';
import DetailPage from './[account_id]/+page.svelte';
import { load as detailLoad } from './[account_id]/+page';
import { actions as detailActions } from './[account_id]/+page.server';

// HUB-291: accounts list, create with write-only credentials, config/quota edit, connection
// test, rotation (add next -> test -> promote -> delete old), lifecycle, and roles.

const goto = vi.fn();
vi.mock('$app/navigation', () => ({
	goto: (...a: unknown[]) => goto(...a),
	invalidateAll: vi.fn()
}));

const SECRET = 'example-passcode-not-real-9f2c';
const ID = 'ct-brandA-in';
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

async function renderNew(search = '') {
	const data = newLoad(event(`/accounts/new${search}`, {}, admin) as never);
	render(NewPage, { props: { data: { access: admin, ...(data as object) } as never } });
	await screen.findByLabelText(/^Account id/);
}
async function renderDetail(a: Access = admin, account = contractMocks.getAccount.example) {
	server.use(mockOperation('getAccount', account));
	const data = await detailLoad(event(`/accounts/${ID}`, { account_id: ID }, a));
	render(DetailPage, { props: { data: { access: a, ...(data as object) } as never } });
	if (!a.tenantScoped) await screen.findByRole('region', { name: 'Summary' });
}

let logged: unknown[][];
beforeEach(() => {
	goto.mockReset();
	takeFlash();
	logged = captureConsole();
});

describe('accounts list', () => {
	it('sends the URL filters and links accounts and vendors', async () => {
		const seen: URL[] = [];
		server.events.on('request:start', ({ request }) => seen.push(new URL(request.url)));
		const data = await listLoad(event('/accounts?vendor_id=clevertap&state=active', {}, admin));
		render(ListPage, { props: { data: { access: admin, ...(data as object) } as never } });
		const table = await screen.findByRole('table', { busy: false });
		expect(
			Object.fromEntries(seen.find((u) => u.pathname.endsWith('/accounts'))!.searchParams)
		).toEqual({
			vendor_id: 'clevertap',
			state: 'active',
			limit: '50'
		});
		expect(within(table).getByRole('link', { name: ID })).toHaveAttribute(
			'href',
			`/accounts/${ID}`
		);
		expect(screen.getByRole('link', { name: 'Create account' })).toHaveAttribute(
			'href',
			'/accounts/new?vendor_id=clevertap'
		);
		server.events.removeAllListeners();
	});
});

describe('create account (write-only credentials)', () => {
	beforeEach(() => useActions(newActions));

	async function fill() {
		await userEvent.type(screen.getByLabelText(/^Account id/), 'ct-brandC-in');
		await userEvent.selectOptions(screen.getByLabelText(/^Vendor/), 'clevertap');
		await userEvent.selectOptions(screen.getByLabelText(/^Region/), 'in');
		await userEvent.type(screen.getByLabelText('Key'), 'passcode');
		await userEvent.type(screen.getByLabelText('Secret value'), SECRET);
		await userEvent.type(screen.getByLabelText(/^Reason/), 'Brand C India workspace');
	}

	it('posts the credentials once, clears them right away and never shows or logs them', async () => {
		const bodies: Record<string, unknown>[] = [];
		server.use(
			http.post(mswPath('/accounts'), async ({ request }) => {
				bodies.push((await request.json()) as never);
				return HttpResponse.json(contractMocks.createAccount.example, { status: 201 });
			})
		);
		await renderNew();
		expect(screen.getByLabelText('Secret value')).toHaveAttribute('type', 'password');
		expect(screen.getByLabelText('Secret value')).toHaveAttribute('autocomplete', 'new-password');
		await fill();
		await userEvent.click(screen.getByRole('button', { name: 'Create account' }));
		await waitFor(() => expect(goto).toHaveBeenCalledWith(`/accounts/${ID}`));
		expect(bodies[0]).toMatchObject({
			account_id: 'ct-brandC-in',
			vendor_id: 'clevertap',
			region: 'in',
			config: {},
			credentials: { passcode: SECRET },
			reason: 'Brand C India workspace'
		});
		expect(secretTraces(SECRET, logged)).toEqual(NOWHERE);
		const flash = takeFlash();
		expect(flash?.changeId).toBe(contractMocks.createAccount.example.change_id);
		expect(JSON.stringify(flash)).not.toContain(SECRET);
	});

	it('on an API error the secret is gone from the page and from the answer', async () => {
		server.use(
			mockError('createAccount', 400, {
				message: `credentials.passcode ${SECRET} rejected`,
				details: [{ pointer: '/credentials/passcode', message: `value ${SECRET} too short` }]
			})
		);
		await renderNew();
		await fill();
		await userEvent.click(screen.getByRole('button', { name: 'Create account' }));
		expect(await screen.findByText(/were cleared when you submitted/)).toBeInTheDocument();
		await waitFor(() => expect(screen.getAllByText(/\[redacted\]/).length).toBeGreaterThan(0));
		expect(screen.getByLabelText('Secret value')).toHaveValue('');
		expect(screen.getByLabelText('Key')).toHaveValue('passcode');
		expect(screen.getByLabelText(/^Account id/)).toHaveValue('ct-brandC-in');
		expect(secretTraces(SECRET, logged)).toEqual(NOWHERE);
	});

	it('preselects the vendor from the URL and offers only its regions', async () => {
		await renderNew('?vendor_id=clevertap');
		expect(screen.getByLabelText(/^Vendor/)).toHaveValue('clevertap');
		const regions = within(screen.getByLabelText(/^Region/)).getAllByRole('option');
		expect(regions.map((o) => o.textContent)).toEqual(['Pick a region', 'in', 'us']);
	});

	it('an auth `none` vendor: no credential inputs, none sent (HUB-436)', async () => {
		const list = contractMocks.listVendors.example;
		server.use(
			mockOperation('listVendors', {
				...list,
				items: [
					...list.items,
					{
						...list.items[0]!,
						vendor_id: 'agify',
						display_name: 'Agify',
						auth_scheme: 'none',
						regions: ['global']
					}
				]
			})
		);
		const bodies: Record<string, unknown>[] = [];
		server.use(
			http.post(mswPath('/accounts'), async ({ request }) => {
				bodies.push((await request.json()) as never);
				return HttpResponse.json(contractMocks.createAccount.example, { status: 201 });
			})
		);
		await renderNew();
		await userEvent.type(screen.getByLabelText(/^Account id/), 'agify-public');
		await userEvent.selectOptions(screen.getByLabelText(/^Vendor/), 'agify');
		await userEvent.selectOptions(screen.getByLabelText(/^Region/), 'global');
		expect(screen.queryByLabelText('Secret value')).toBeNull();
		expect(screen.queryByLabelText(/^Credential expires/)).toBeNull();
		expect(screen.getByRole('note')).toHaveTextContent('agify is a public API (auth scheme none)');
		await userEvent.type(screen.getByLabelText(/^Reason/), 'Public age estimate API');
		await userEvent.click(screen.getByRole('button', { name: 'Create account' }));
		await waitFor(() => expect(bodies).toHaveLength(1));
		expect(bodies[0]).toMatchObject({
			account_id: 'agify-public',
			vendor_id: 'agify',
			region: 'global'
		});
		expect('credentials' in bodies[0]!).toBe(false);
	});

	it('the action refuses a plain (no-JS) post: credentials only travel in the background', async () => {
		const form = new FormData();
		form.set('reason', 'Brand C India workspace');
		const r = await runAction(newActions, 'create', form, { enhanced: false });
		expect(r.type).toBe('failure');
		expect(JSON.stringify(r)).toContain('needs JavaScript');
	});
});

describe('account of an auth none vendor (HUB-436)', () => {
	it('says there is nothing to store or rotate, and offers no rotation', async () => {
		const vendor = contractMocks.getVendor.example;
		server.use(mockOperation('getVendor', { ...vendor, auth_scheme: 'none' }));
		await renderDetail(admin, { ...contractMocks.getAccount.example, credentials: [] });
		expect(await screen.findByText(/public API \(auth scheme none\)/)).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Add next credential' })).toBeNull();
	});
});

describe('account detail', () => {
	beforeEach(() => useActions(detailActions, { params: { account_id: ID } }));

	it('shows slots as metadata only, config, integrations and the dead letters link', async () => {
		await renderDetail();
		const slots = screen.getByRole('region', { name: 'Credential slots' });
		expect(slots).toHaveTextContent('Active');
		expect(slots).toHaveTextContent('sha256:9f2c1e');
		expect(screen.getByText(/"project_id": "4RW-XXX-XXXX"/)).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'int_evt_brandA v7' })).toHaveAttribute(
			'href',
			'/integrations/int_evt_brandA/versions/7'
		);
		expect(screen.getByRole('link', { name: '1' })).toHaveAttribute(
			'href',
			`/dlq?account_id=${ID}`
		);
	});

	it('edit sends a merge-patch of what changed with the ETag as If-Match', async () => {
		const seen: { body: unknown; ifMatch: string | null }[] = [];
		server.use(
			http.patch(mswPath('/accounts/:account_id'), async ({ request }) => {
				seen.push({ body: await request.json(), ifMatch: request.headers.get('If-Match') });
				return HttpResponse.json(contractMocks.updateAccount.example);
			})
		);
		server.use(
			http.get(mswPath('/accounts/:account_id'), () =>
				HttpResponse.json(contractMocks.getAccount.example, { headers: { ETag: '"a1"' } })
			)
		);
		const data = await detailLoad(event(`/accounts/${ID}`, { account_id: ID }, admin));
		render(DetailPage, { props: { data: { access: admin, ...(data as object) } as never } });
		await userEvent.click(await screen.findByRole('button', { name: 'Edit config and quota' }));
		const dialog = screen.getByRole('dialog');
		const rps = within(dialog).getByLabelText('Quota (requests/s)');
		await userEvent.clear(rps);
		await userEvent.type(rps, '80');
		await userEvent.type(within(dialog).getByLabelText(/^Reason/), 'Vendor raised our quota');
		await userEvent.click(within(dialog).getByRole('button', { name: 'Save changes' }));
		await waitFor(() => expect(seen).toHaveLength(1));
		expect(seen[0]).toEqual({
			body: { quota: { rps: 80 }, reason: 'Vendor raised our quota' },
			ifMatch: '"a1"'
		});
		expect(await screen.findByText(/Saved ct-brandA-in/)).toBeInTheDocument();
	});

	it('rotates: add next (secret cleared) -> test -> promote -> delete old, each with a change id', async () => {
		const posts: Record<string, unknown> = {};
		const capture = (path: string, answer: object) =>
			http.post(mswPath(path), async ({ request }) => {
				posts[path] = await request.json();
				return HttpResponse.json(answer);
			});
		server.use(
			capture('/accounts/:account_id/credentials/next', contractMocks.addNextCredential.example),
			capture('/accounts/:account_id/test-connection', contractMocks.testAccountConnection.example)
		);
		await renderDetail();
		expect(screen.getByRole('list', { name: 'Rotation steps' })).toHaveTextContent(
			'Now Add the next'
		);
		await userEvent.click(screen.getByRole('button', { name: 'Add next credential' }));
		let dialog = screen.getByRole('dialog', { name: `Add the next credential to ${ID}` });
		await userEvent.type(within(dialog).getByLabelText('Key'), 'passcode');
		await userEvent.type(within(dialog).getByLabelText('Secret value'), SECRET);
		await userEvent.type(within(dialog).getByLabelText(/^Reason/), 'Quarterly passcode rotation');
		await userEvent.click(within(dialog).getByRole('button', { name: 'Store next credential' }));
		expect(await screen.findByText(/Stored the next credential/)).toBeInTheDocument();
		expect(posts['/accounts/:account_id/credentials/next']).toEqual({
			credentials: { passcode: SECRET },
			expires_at: null,
			reason: 'Quarterly passcode rotation'
		});
		expect(screen.getByText(contractMocks.addNextCredential.example.change_id)).toBeInTheDocument();
		expect(secretTraces(SECRET, logged)).toEqual(NOWHERE);

		// The page reloads the account; it now has a next slot (v4).
		const withNext = {
			...contractMocks.getAccount.example,
			credentials: contractMocks.addNextCredential.example.credentials
		};
		document.body.innerHTML = '';
		await renderDetail(admin, withNext);
		expect(screen.getByRole('list', { name: 'Rotation steps' })).toHaveTextContent('Now Test it');
		const test = screen.getByRole('form', { name: `Test connection of ${ID}` });
		expect(within(test).getByLabelText('Credential')).toHaveValue('next');
		await userEvent.click(within(test).getByRole('button', { name: 'Test connection' }));
		expect(await screen.findByText('Connection works')).toBeInTheDocument();
		expect(posts['/accounts/:account_id/test-connection']).toEqual({ slot: 'next' });
		expect(screen.getByRole('list', { name: 'Rotation steps' })).toHaveTextContent(
			'Now Promote it'
		);

		const promotes: unknown[] = [];
		server.use(
			http.post(mswPath('/accounts/:account_id/credentials/promote'), async ({ request }) => {
				promotes.push(await request.json());
				return HttpResponse.json(contractMocks.promoteNextCredential.example);
			})
		);
		await userEvent.click(screen.getByRole('button', { name: 'Promote v4' }));
		dialog = screen.getByRole('dialog', { name: `Promote credential v4 of ${ID}` });
		expect(within(dialog).queryByRole('note')).toBeNull(); // tested above
		await userEvent.type(within(dialog).getByLabelText(/^Reason/), 'Connection test passed');
		await userEvent.click(within(dialog).getByRole('button', { name: 'Promote v4' }));
		expect(await screen.findByText(/Promoted the next credential/)).toBeInTheDocument();
		expect(promotes[0]).toEqual({ expected_next_version: 4, reason: 'Connection test passed' });

		document.body.innerHTML = '';
		await renderDetail(admin, {
			...contractMocks.getAccount.example,
			credentials: contractMocks.promoteNextCredential.example.credentials
		});
		const deletes: unknown[] = [];
		server.use(
			http.post(mswPath('/accounts/:account_id/credentials/delete-old'), async ({ request }) => {
				deletes.push(await request.json());
				return HttpResponse.json(contractMocks.deleteOldCredential.example);
			})
		);
		await userEvent.click(screen.getByRole('button', { name: 'Delete previous credential v3' }));
		dialog = screen.getByRole('dialog', { name: `Delete credential v3 of ${ID}` });
		await userEvent.type(within(dialog).getByLabelText(/^Reason/), 'No auth failures for 15 min');
		const confirm = within(dialog).getByRole('button', { name: 'Delete v3' });
		expect(confirm).toBeDisabled();
		await userEvent.type(within(dialog).getByLabelText(/^Type/), ID);
		await userEvent.click(confirm);
		expect(await screen.findByText(/Deleted the previous credential/)).toBeInTheDocument();
		expect(deletes[0]).toEqual({
			expected_previous_version: 3,
			reason: 'No auth failures for 15 min'
		});
	});

	it('promoting an untested credential warns first', async () => {
		await renderDetail(admin, {
			...contractMocks.getAccount.example,
			credentials: contractMocks.addNextCredential.example.credentials
		});
		await userEvent.click(screen.getByRole('button', { name: 'Promote v4' }));
		expect(within(screen.getByRole('dialog')).getByRole('note')).toHaveTextContent(
			'has not passed a connection test'
		);
	});

	it('retire: id typed, RETIRE_BLOCKED shows the API reason (not a reload prompt)', async () => {
		server.use(
			mockError('transitionAccountLifecycle', 409, {
				code: 'RETIRE_BLOCKED',
				message: 'Account is still referenced by 3 pinned requests'
			})
		);
		await renderDetail(admin, { ...contractMocks.getAccount.example, state: 'draining' });
		await userEvent.click(screen.getByRole('button', { name: 'Retire account' }));
		const dialog = screen.getByRole('dialog', { name: `Retire account: ${ID}` });
		await userEvent.type(within(dialog).getByLabelText(/^Reason/), 'Brand moved to another vendor');
		await userEvent.type(within(dialog).getByLabelText(/^Type/), ID);
		await userEvent.click(within(dialog).getByRole('button', { name: 'Retire account' }));
		const alert = await within(dialog).findByRole('alert');
		expect(alert).toHaveTextContent('RETIRE_BLOCKED');
		expect(alert).toHaveTextContent('3 pinned requests');
		expect(alert).not.toHaveTextContent('Someone else changed');
	});

	it('the lifecycle action re-checks the typed id on the server', async () => {
		const form = new FormData();
		form.set('reason', 'Brand moved to another vendor');
		form.set('target_state', 'retired');
		form.set('confirm', 'wrong');
		const r = await runAction(detailActions, 'lifecycle', form, { params: { account_id: ID } });
		expect(r).toMatchObject({
			type: 'failure',
			data: { fields: { confirm: `Type ${ID} to confirm.` } }
		});
	});
});

describe('roles (x-required-role: testAccountConnection = editor, everything else admin)', () => {
	beforeEach(() => useActions(detailActions, { params: { account_id: ID } }));
	const rotating = {
		...contractMocks.getAccount.example,
		state: 'draining' as const,
		credentials: [
			...contractMocks.promoteNextCredential.example.credentials,
			{
				slot: 'next' as const,
				cred_version: 5,
				fingerprint: 'sha256:aa',
				created_at: '2026-11-01T00:00:00Z'
			}
		]
	};
	const controls = [
		{ name: /^Edit config and quota$/, min: 'admin' },
		{ name: /^Replace next credential/, min: 'admin' },
		{ name: /^Promote v5$/, min: 'admin' },
		{ name: /^Delete previous credential/, min: 'admin' },
		{ name: /^Retire account$/, min: 'admin' },
		{ name: /^Return to active$/, min: 'admin' },
		{ name: /^Test connection$/, min: 'editor' }
	] as const;

	it.each<Role>(['admin', 'editor', 'viewer'])(
		'%s sees exactly the controls its role allows',
		async (role) => {
			await renderDetail(access(role), rotating);
			for (const c of controls) {
				const found = screen.queryAllByRole('button', { name: c.name });
				expect({ control: String(c.name), shown: found.length > 0 }).toEqual({
					control: String(c.name),
					shown: hasRole([role], c.min)
				});
			}
		}
	);

	it('tenant viewers get a note and no account call', async () => {
		const calls: string[] = [];
		server.events.on('request:start', ({ request }) => calls.push(request.url));
		const data = await detailLoad(
			event(`/accounts/${ID}`, { account_id: ID }, access('tenant_viewer'))
		);
		render(DetailPage, {
			props: { data: { access: access('tenant_viewer'), ...(data as object) } as never }
		});
		expect(screen.getByRole('note')).toHaveTextContent('tenant viewers cannot see them');
		expect(calls).toEqual([]);
		server.events.removeAllListeners();
	});

	it('create is a read-only note below admin', async () => {
		render(NewPage, {
			props: {
				data: { access: access('editor'), vendorId: '', vendors: new Promise(() => {}) } as never
			}
		});
		expect(screen.getByRole('note')).toHaveTextContent('Creating accounts needs the Admin role');
	});
});

import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mockOperation, mswPath } from '$lib/api/mocks/handlers';
import { presets } from '$lib/api/mocks/scenarios';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import type { Role } from '$lib/api/types';
import { invalidations, runAction, useActions } from '$lib/testing/forms';
import { captureConsole, NOWHERE, secretTraces } from '$lib/testing/secrets';
import Page from './+page.svelte';
import { load } from './+page';
import { actions } from './+page.server';

// HUB-272: callback (registrar) endpoints; a new signing secret is shown exactly once.

const invalidateAll = vi.fn();
vi.mock('$app/navigation', () => ({ invalidateAll: () => invalidateAll(), goto: vi.fn() }));

const ID = 'orders-service';
const EP = 'reg_orders_cb';
const REASON = 'Callbacks for async integrations';
const CREATED = contractMocks.createRegistrarEndpoint.example.signing_secret;
const ROTATED = contractMocks.rotateRegistrarSecret.example.signing_secret;

let logged: unknown[][];
beforeEach(() => {
	useActions(actions, { params: { tenant_id: ID } });
	invalidateAll.mockReset();
	invalidations.count = 0;
	logged = captureConsole();
	localStorage.clear();
	sessionStorage.clear();
});

async function renderPage(access: Access = accessFromRoles(['admin'])) {
	const data = await load({
		fetch,
		url: new URL(`http://console.test/tenants/${ID}`),
		params: { tenant_id: ID },
		parent: async () => ({ access })
	} as never);
	render(Page, { props: { data: { access, ...(data as object) } as never } });
	return screen.findByRole('table', { name: `Callback endpoints of ${ID}`, busy: false });
}

/** No signing secret anywhere in the page (the list never carries one). */
function noSecretAnywhere() {
	for (const secret of [CREATED, ROTATED]) expect(secretTraces(secret, logged)).toEqual(NOWHERE);
	expect(document.documentElement.outerHTML).not.toContain('whsec_');
}

/** Ticks "I have stored it" and closes the reveal. */
async function done(user: ReturnType<typeof userEvent.setup>, reveal: HTMLElement) {
	const button = within(reveal).getByRole('button', { name: 'Done' });
	expect(button).toBeDisabled();
	await user.click(within(reveal).getByLabelText('I have stored it somewhere safe'));
	await user.click(button);
}

describe('list', () => {
	it('shows URL, state, secret version and subscribers; never a secret', async () => {
		const table = await renderPage(accessFromRoles(['tenant_viewer'], [ID]));
		const row = within(table).getAllByRole('row')[1]!;
		expect(row).toHaveTextContent(EP);
		expect(row).toHaveTextContent('Enabled');
		expect(row).toHaveTextContent('https://orders.internal/hub/callbacks');
		expect(row).toHaveTextContent('Version 1');
		expect(row).toHaveTextContent('2');
		expect(within(table).queryByRole('button')).toBeNull();
		noSecretAnywhere();
	});

	it.each<Role>(['editor', 'viewer'])('%s has no endpoint controls (admin only)', async (role) => {
		const table = await renderPage(accessFromRoles([role]));
		expect(within(table).queryByRole('button')).toBeNull();
		expect(screen.queryByRole('button', { name: 'Register endpoint' })).toBeNull();
	});
});

describe('register', () => {
	async function openRegister() {
		const user = userEvent.setup();
		await renderPage();
		await user.click(screen.getByRole('button', { name: 'Register endpoint' }));
		return {
			user,
			dialog: await screen.findByRole('dialog', { name: `Register a callback endpoint for ${ID}` })
		};
	}

	it('sends RegistrarEndpointCreate and shows the signing secret exactly once', async () => {
		let sent: unknown;
		server.use(
			http.post(mswPath('/registrar-endpoints'), async ({ request }) => {
				sent = await request.json();
				return HttpResponse.json(contractMocks.createRegistrarEndpoint.example, { status: 201 });
			})
		);
		const { user, dialog } = await openRegister();
		await user.type(within(dialog).getByLabelText(/Endpoint id/), EP);
		await user.type(
			within(dialog).getByLabelText(/Callback URL/),
			'http://orders.internal/hub/callbacks'
		);
		await user.tab();
		expect(within(dialog).getByLabelText(/Callback URL/)).toHaveAccessibleDescription(
			/Use an https:\/\/ URL/
		);
		await user.clear(within(dialog).getByLabelText(/Callback URL/));
		await user.type(
			within(dialog).getByLabelText(/Callback URL/),
			'https://orders.internal/hub/callbacks'
		);
		await user.type(within(dialog).getByLabelText('Description'), 'Orders callbacks');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Register' }));
		const reveal = await screen.findByRole('dialog', {
			name: 'Copy the callback signing secret of reg_orders_cb'
		});
		const field = within(reveal).getByLabelText('Signing secret version 1 of reg_orders_cb');
		expect(field).toHaveValue(CREATED);
		expect(field).toHaveAttribute('readonly');
		expect(field).toHaveFocus();
		expect(reveal).toHaveTextContent('This is the only time it is shown.');
		expect(reveal).toHaveTextContent('verify the X-Hub-Signature header of every callback');
		await user.click(within(reveal).getByRole('button', { name: 'Copy' }));
		expect(await navigator.clipboard.readText()).toBe(CREATED);
		const notice = screen.getByText(/Registered callback endpoint reg_orders_cb/);
		expect(notice).toHaveTextContent('signing secret version 1');
		const status = notice.closest('[role="status"]')!;
		expect(status).toHaveTextContent('chg_01J9Z4B2C3D4E5F6G7H8J9K0NF');
		expect(status.textContent).not.toContain(CREATED);
		await user.keyboard('{Escape}');
		expect(reveal).toBeInTheDocument();
		await done(user, reveal);
		expect(screen.queryByRole('dialog')).toBeNull();
		expect(screen.getByRole('button', { name: 'Register endpoint' })).toHaveFocus();
		// Never applied as the page form (update() not called); only invalidateAll reloaded.
		expect(invalidations.count).toBe(0);
		expect(invalidateAll).toHaveBeenCalled();
		expect(sent).toEqual({
			endpoint_id: EP,
			tenant_id: ID,
			url: 'https://orders.internal/hub/callbacks',
			description: 'Orders callbacks',
			reason: REASON
		});
		noSecretAnywhere();
	});

	it('the SSRF guard error (pointer /url) lands on the URL field', async () => {
		server.use(...presets['registrar-url-private']!());
		const { user, dialog } = await openRegister();
		await user.type(within(dialog).getByLabelText(/Endpoint id/), EP);
		await user.type(within(dialog).getByLabelText(/Callback URL/), 'https://10.0.0.12/cb');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Register' }));
		const url = within(dialog).getByLabelText(/Callback URL/);
		await waitFor(() =>
			expect(url).toHaveAccessibleDescription(/host resolves to a private or link-local address/)
		);
		expect(url).toHaveAttribute('aria-invalid', 'true');
		expect(within(dialog).getByRole('alert')).toHaveTextContent('VALIDATION_FAILED');
	});
});

describe('row actions', () => {
	async function pick(name: RegExp) {
		const user = userEvent.setup();
		const table = await renderPage();
		await user.click(within(table).getByRole('button', { name }));
		return { user, dialog: await screen.findByRole('dialog') };
	}

	it('edit of the description works when the stored URL is a dev http one (HUB-289)', async () => {
		let sent: unknown;
		const dev = {
			...contractMocks.listRegistrarEndpoints.example.items[0]!,
			url: 'http://127.0.0.1:8097/hub/callbacks'
		};
		server.use(
			mockOperation('listRegistrarEndpoints', { items: [dev], next_cursor: null }),
			http.patch(mswPath('/registrar-endpoints/:endpoint_id'), async ({ request }) => {
				sent = await request.json();
				return HttpResponse.json(contractMocks.updateRegistrarEndpoint.example);
			})
		);
		const { user, dialog } = await pick(/^Edit reg_orders_cb/);
		await user.clear(within(dialog).getByLabelText(/^Description/));
		await user.type(within(dialog).getByLabelText(/^Description/), 'QA receiver');
		await user.type(within(dialog).getByLabelText(/Reason/), 'Name the dev receiver');
		const save = within(dialog).getByRole('button', { name: 'Save changes' });
		expect(save).toBeEnabled();
		await user.click(save);
		await waitFor(() =>
			expect(sent).toEqual({ description: 'QA receiver', reason: 'Name the dev receiver' })
		);
	});

	it('edit sends only the new URL; the SSRF error lands on the field there too', async () => {
		let sent: unknown;
		server.use(
			http.patch(mswPath('/registrar-endpoints/:endpoint_id'), async ({ request }) => {
				sent = await request.json();
				return HttpResponse.json(contractMocks.updateRegistrarEndpoint.example);
			})
		);
		const { user, dialog } = await pick(/^Edit reg_orders_cb/);
		expect(dialog).toHaveAccessibleName(`Edit callback endpoint ${EP}`);
		const url = within(dialog).getByLabelText(/Callback URL/);
		expect(url).toHaveValue('https://orders.internal/hub/callbacks');
		await user.type(within(dialog).getByLabelText(/Reason/), 'New callback route');
		expect(within(dialog).getByRole('button', { name: 'Save changes' })).toBeDisabled();
		await user.clear(url);
		await user.type(url, 'https://orders.internal/hub/v2/callbacks');
		await user.click(within(dialog).getByRole('button', { name: 'Save changes' }));
		expect(await screen.findByText(/Saved reg_orders_cb/)).toBeInTheDocument();
		expect(sent).toEqual({
			url: 'https://orders.internal/hub/v2/callbacks',
			reason: 'New callback route'
		});
	});

	it('rotate sends the overlap, shows the new secret once and says when both signatures stop', async () => {
		let sent: unknown;
		server.use(
			http.post(mswPath('/registrar-endpoints/:endpoint_id/rotate-secret'), async ({ request }) => {
				sent = await request.json();
				return HttpResponse.json(contractMocks.rotateRegistrarSecret.example);
			})
		);
		const { user, dialog } = await pick(/^Rotate secret of reg_orders_cb/);
		expect(dialog).toHaveTextContent('Mints signing secret version 2');
		await user.selectOptions(within(dialog).getByLabelText('Overlap'), '1 hour');
		await user.type(within(dialog).getByLabelText(/Reason/), 'Scheduled rotation');
		await user.click(within(dialog).getByRole('button', { name: 'Rotate secret' }));
		const reveal = await screen.findByRole('dialog', {
			name: 'Copy the new callback signing secret of reg_orders_cb'
		});
		expect(within(reveal).getByLabelText('Signing secret version 2 of reg_orders_cb')).toHaveValue(
			ROTATED
		);
		expect(reveal).toHaveTextContent(
			'Until 2026-11-01 10:00 UTC, callbacks carry both signatures (version 2 and 1)'
		);
		const notice = screen.getByText(/Rotated the signing secret of reg_orders_cb to version 2/);
		expect(notice).toHaveTextContent('Until 2026-11-01 10:00 UTC, callbacks carry both signatures');
		expect(sent).toEqual({ overlap_ms: 3_600_000, reason: 'Scheduled rotation' });
		await done(user, reveal);
		noSecretAnywhere();
		expect(invalidations.count).toBe(0);
	});

	it('disable sends disabled: true', async () => {
		let sent: unknown;
		server.use(
			http.patch(mswPath('/registrar-endpoints/:endpoint_id'), async ({ request }) => {
				sent = await request.json();
				return HttpResponse.json({
					...contractMocks.updateRegistrarEndpoint.example,
					endpoint: { ...contractMocks.updateRegistrarEndpoint.example.endpoint, disabled: true }
				});
			})
		);
		const { user, dialog } = await pick(/^Disable reg_orders_cb/);
		expect(dialog).toHaveTextContent('2 integration versions subscribe');
		await user.type(within(dialog).getByLabelText(/Reason/), 'Receiver under maintenance');
		await user.click(within(dialog).getByRole('button', { name: 'Disable' }));
		expect(await screen.findByText(/Disabled reg_orders_cb/)).toBeInTheDocument();
		expect(sent).toEqual({ disabled: true, reason: 'Receiver under maintenance' });
	});

	it('delete needs the id typed, sends the reason as a query parameter and explains 409', async () => {
		let url: URL | undefined;
		server.use(
			http.delete(mswPath('/registrar-endpoints/:endpoint_id'), ({ request }) => {
				url = new URL(request.url);
				return HttpResponse.json(contractMocks.deleteRegistrarEndpoint.errors['409'], {
					status: 409
				});
			})
		);
		const { user, dialog } = await pick(/^Delete reg_orders_cb/);
		expect(dialog).toHaveTextContent('the Hub refuses the delete until none does');
		await user.type(within(dialog).getByLabelText(/Reason/), 'Endpoint replaced by v2');
		const confirm = within(dialog).getByRole('button', { name: 'Delete' });
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByLabelText(/to confirm/), EP);
		await user.click(confirm);
		// HUB-289: a 409 on delete means "still in use", not a race: the API's message, no reload.
		const alert = await within(dialog).findByRole('alert');
		expect(alert).toHaveTextContent('Could not delete');
		expect(alert).toHaveTextContent(
			contractMocks.deleteRegistrarEndpoint.errors['409']!.error.message
		);
		expect(alert).not.toHaveTextContent('Someone else changed');
		expect(within(dialog).queryByRole('button', { name: 'Reload' })).toBeNull();
		expect(url?.pathname).toMatch(/\/registrar-endpoints\/reg_orders_cb$/);
		expect(url?.searchParams.get('reason')).toBe('Endpoint replaced by v2');
	});
});

describe('endpoint actions', () => {
	const form = (f: Record<string, string>) => {
		const d = new FormData();
		for (const [k, v] of Object.entries(f)) d.set(k, v);
		return d;
	};

	const fields = {
		reason: REASON,
		endpoint_id: EP,
		url: 'https://orders.internal/hub/callbacks',
		overlap_ms: '86400000'
	};

	it.each([
		['createEndpoint', CREATED],
		['rotateSecret', ROTATED]
	])('%s passes the new secret through once, logging nothing', async (name, secret) => {
		const r = await runAction(actions, name, form(fields), {
			params: { tenant_id: ID },
			enhanced: true
		});
		expect(r).toMatchObject({
			type: 'success',
			data: { result: { signing_secret: secret, endpoint: { endpoint_id: EP } } }
		});
		expect(logged).toEqual([]);
	});

	it.each(['createEndpoint', 'rotateSecret'])(
		'%s refuses a plain (no-JS) post before calling the API',
		async (name) => {
			const calls: string[] = [];
			server.events.on('request:start', ({ request }) => calls.push(request.url));
			const r = await runAction(actions, name, form(fields), { params: { tenant_id: ID } });
			expect(r).toMatchObject({ type: 'failure', status: 400 });
			expect(calls).toEqual([]);
			server.events.removeAllListeners();
		}
	);

	it('create refuses a non-https URL without calling the API', async () => {
		const calls: string[] = [];
		server.events.on('request:start', ({ request }) => calls.push(request.url));
		const r = await runAction(
			actions,
			'createEndpoint',
			form({ reason: REASON, endpoint_id: EP, url: 'http://orders.internal/cb' }),
			{ params: { tenant_id: ID }, enhanced: true }
		);
		expect(r).toMatchObject({ type: 'failure', data: { fields: { url: 'Use an https:// URL.' } } });
		expect(calls).toEqual([]);
		server.events.removeAllListeners();
	});

	it('delete re-checks the typed id', async () => {
		server.use(mockError('deleteRegistrarEndpoint', 500));
		const r = await runAction(
			actions,
			'deleteEndpoint',
			form({ reason: REASON, endpoint_id: EP, confirm: 'reg' }),
			{ params: { tenant_id: ID } }
		);
		expect(r).toMatchObject({ data: { fields: { confirm: `Type ${EP} to confirm.` } } });
	});
});

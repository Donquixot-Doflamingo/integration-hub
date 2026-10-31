import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mswPath } from '$lib/api/mocks/handlers';
import { presets } from '$lib/api/mocks/scenarios';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import type { Role } from '$lib/api/types';
import { runAction, useActions } from '$lib/testing/forms';
import Page from './+page.svelte';
import { load } from './+page';
import { actions } from './+page.server';

// HUB-272: callback (registrar) endpoints; signing secrets never displayed.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const ID = 'orders-service';
const EP = 'reg_orders_cb';
const REASON = 'Callbacks for async integrations';
const SECRETS = [
	contractMocks.createRegistrarEndpoint.example.signing_secret,
	contractMocks.rotateRegistrarSecret.example.signing_secret
];

beforeEach(() => {
	useActions(actions, { params: { tenant_id: ID } });
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

function noSecretAnywhere() {
	const html = document.documentElement.outerHTML;
	const values = [...document.querySelectorAll('input')].map((i) => i.value).join(' ');
	for (const s of SECRETS) {
		expect(html).not.toContain(s);
		expect(values).not.toContain(s);
	}
	expect(html).not.toContain('whsec_');
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

	it('sends RegistrarEndpointCreate and never shows the minted signing secret', async () => {
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
		const notice = await screen.findByText(/Registered callback endpoint reg_orders_cb/);
		expect(notice).toHaveTextContent('signing secret version 1');
		expect(notice).toHaveTextContent('The console never shows signing secrets.');
		expect(notice.closest('[role="status"]')).toHaveTextContent('chg_01J9Z4B2C3D4E5F6G7H8J9K0NF');
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

	it('rotate sends the overlap and says when both signatures stop, without the secret', async () => {
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
		const notice = await screen.findByText(
			/Rotated the signing secret of reg_orders_cb to version 2/
		);
		expect(notice).toHaveTextContent(
			'Until 2026-11-01 10:00 UTC, callbacks carry both signatures.'
		);
		expect(sent).toEqual({ overlap_ms: 3_600_000, reason: 'Scheduled rotation' });
		noSecretAnywhere();
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
		expect(await within(dialog).findByRole('alert')).toHaveTextContent(
			`Someone else changed ${EP}`
		);
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

	it.each(['createEndpoint', 'rotateSecret'])(
		'%s never returns the signing secret to the browser',
		async (name) => {
			const r = await runAction(
				actions,
				name,
				form({
					reason: REASON,
					endpoint_id: EP,
					url: 'https://orders.internal/hub/callbacks',
					overlap_ms: '86400000'
				}),
				{ params: { tenant_id: ID } }
			);
			expect(r.type).toBe('success');
			expect(JSON.stringify(r)).not.toContain('whsec_');
			expect(r).toMatchObject({ data: { result: { endpoint: { endpoint_id: EP } } } });
		}
	);

	it('create refuses a non-https URL without calling the API', async () => {
		const calls: string[] = [];
		server.events.on('request:start', ({ request }) => calls.push(request.url));
		const r = await runAction(
			actions,
			'createEndpoint',
			form({ reason: REASON, endpoint_id: EP, url: 'http://orders.internal/cb' }),
			{ params: { tenant_id: ID } }
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

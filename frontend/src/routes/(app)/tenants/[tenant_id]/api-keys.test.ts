import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mockOperation, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import type { Role } from '$lib/api/types';
import { invalidations, runAction, useActions } from '$lib/testing/forms';
import { captureConsole, NOWHERE, secretTraces } from '$lib/testing/secrets';
import Page from './+page.svelte';
import { load } from './+page';
import { actions } from './+page.server';

// HUB-271: API keys with the secret shown exactly once.

const invalidateAll = vi.fn();
vi.mock('$app/navigation', () => ({ invalidateAll: () => invalidateAll(), goto: vi.fn() }));

const ID = 'orders-service';
const REASON = 'Initial production key, OPS-201';
const CREATED = contractMocks.createApiKey.example.secret;
const ROTATED = contractMocks.rotateApiKey.example.secret;

let logged: unknown[][];

beforeEach(() => {
	useActions(actions, { params: { tenant_id: ID } });
	invalidateAll.mockReset();
	invalidations.count = 0;
	logged = captureConsole();
	localStorage.clear();
	sessionStorage.clear();
});

afterEach(() => {
	server.events.removeAllListeners();
});

async function renderPage(access: Access = accessFromRoles(['admin']), search = '') {
	const data = await load({
		fetch,
		url: new URL(`http://console.test/tenants/${ID}${search}`),
		params: { tenant_id: ID },
		parent: async () => ({ access })
	} as never);
	render(Page, { props: { data: { access, ...(data as object) } as never } });
	return screen.findByRole('table', { name: `API keys of ${ID}`, busy: false });
}

const traces = (secret: string) => secretTraces(secret, logged);
const NONE = NOWHERE;

async function createKey(user = userEvent.setup()) {
	const table = await renderPage();
	await user.click(screen.getByRole('button', { name: 'Create API key' }));
	const dialog = await screen.findByRole('dialog', { name: `Create an API key for ${ID}` });
	await user.type(within(dialog).getByLabelText('Description'), 'orders prod');
	await user.type(within(dialog).getByLabelText(/Reason/), REASON);
	await user.click(within(dialog).getByRole('button', { name: 'Create key' }));
	const reveal = await screen.findByRole('dialog', { name: `Copy the new API key for ${ID}` });
	return { user, table, reveal };
}

describe('list', () => {
	it('shows metadata only, with state in text', async () => {
		const table = await renderPage(accessFromRoles(['tenant_viewer'], [ID]));
		const row = within(table).getAllByRole('row')[1]!;
		expect(row).toHaveTextContent('hk_live_7Qm2');
		expect(row).toHaveTextContent('Active');
		expect(row).toHaveTextContent('invoke');
		expect(row).toHaveTextContent('2026-10-31 09:14 UTC');
		expect(within(table).queryByRole('button')).toBeNull();
		expect(screen.queryByRole('button', { name: 'Create API key' })).toBeNull();
	});

	it('filters by state through the URL', async () => {
		const seen: URLSearchParams[] = [];
		server.events.on('request:start', ({ request }) => {
			const u = new URL(request.url);
			if (u.pathname.endsWith('/admin/v1/api-keys')) seen.push(u.searchParams);
		});
		await renderPage(accessFromRoles(['viewer']), '?key_state=revoked');
		expect(Object.fromEntries(seen[0]!)).toEqual({
			tenant_id: ID,
			limit: '200',
			state: 'revoked'
		});
		expect(screen.getByLabelText('State')).toHaveValue('revoked');
	});

	it.each<Role>(['editor', 'viewer'])('%s has no key controls (admin only)', async (role) => {
		const table = await renderPage(accessFromRoles([role]));
		expect(within(table).queryByRole('button')).toBeNull();
		expect(screen.queryByRole('button', { name: 'Create API key' })).toBeNull();
	});
});

describe('create: the secret is shown exactly once', () => {
	it('sends ApiKeyCreate with the reason and reveals the key with a copy button and a warning', async () => {
		let sent: unknown;
		server.use(
			http.post(mswPath('/api-keys'), async ({ request }) => {
				sent = await request.json();
				return HttpResponse.json(contractMocks.createApiKey.example, { status: 201 });
			})
		);
		const { user, reveal } = await createKey();
		expect(sent).toEqual({
			tenant_id: ID,
			description: 'orders prod',
			scopes: ['invoke'],
			expires_at: null,
			reason: REASON
		});
		const field = within(reveal).getByLabelText(/New API key hk_live_7Qm2/);
		expect(field).toHaveValue(CREATED);
		expect(field).toHaveAttribute('readonly');
		expect(field).toHaveFocus();
		expect(reveal).toHaveTextContent('This is the only time it is shown.');
		expect(reveal).toHaveTextContent('nobody can show it again');
		await user.click(within(reveal).getByRole('button', { name: 'Copy' }));
		// user-event's clipboard stub stands in for the browser's.
		expect(await navigator.clipboard.readText()).toBe(CREATED);
		expect(within(reveal).getByText('Copied to the clipboard.')).toBeInTheDocument();
		// The change notice has the ids, never the secret.
		const notice = screen.getByText(/Created API key hk_live_7Qm2/).closest('[role="status"]')!;
		expect(notice).toHaveTextContent('chg_01J9Z4B2C3D4E5F6G7H8J9K0NC');
		expect(notice.textContent).not.toContain(CREATED);
	});

	it('is gone from the DOM, inputs, storage, URL, history and logs after Done, and nothing can show it again', async () => {
		const { user, reveal } = await createKey();
		const done = within(reveal).getByRole('button', { name: 'Done' });
		expect(done).toBeDisabled();
		await user.keyboard('{Escape}');
		expect(screen.getByRole('dialog', { name: /Copy the new API key/ })).toBeInTheDocument();
		await user.click(within(reveal).getByLabelText('I have stored it somewhere safe'));
		await user.click(done);
		expect(screen.queryByRole('dialog')).toBeNull();
		expect(traces(CREATED)).toEqual(NONE);
		// Focus goes back to where the operator started.
		expect(screen.getByRole('button', { name: 'Create API key' })).toHaveFocus();
		// The answer never became the page's `form` (update() would apply it): only invalidateAll ran.
		expect(invalidations.count).toBe(0);
		expect(invalidateAll).toHaveBeenCalled();
		// No control re-displays it; the list and a new dialog show the prefix at most.
		expect(screen.queryByRole('button', { name: /show|reveal/i })).toBeNull();
		await user.click(screen.getByRole('button', { name: 'Create API key' }));
		expect(traces(CREATED)).toEqual(NONE);
	});

	it('API failure: no reveal, the dialog keeps the reason and shows the request id', async () => {
		server.use(mockError('createApiKey', 403));
		const user = userEvent.setup();
		await renderPage();
		await user.click(screen.getByRole('button', { name: 'Create API key' }));
		const dialog = await screen.findByRole('dialog', { name: `Create an API key for ${ID}` });
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Create key' }));
		const alert = await within(dialog).findByRole('alert');
		expect(alert).toHaveTextContent('FORBIDDEN');
		expect(alert).toHaveTextContent(contractMocks.createApiKey.errors['403']!.error.request_id);
		expect(screen.queryByRole('dialog', { name: /Copy the new API key/ })).toBeNull();
	});

	it('needs a scope', async () => {
		const user = userEvent.setup();
		await renderPage();
		await user.click(screen.getByRole('button', { name: 'Create API key' }));
		const dialog = await screen.findByRole('dialog', { name: `Create an API key for ${ID}` });
		await user.click(within(dialog).getByLabelText(/^invoke/));
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		expect(within(dialog).getByRole('button', { name: 'Create key' })).toBeDisabled();
		expect(within(dialog).getByText('Pick at least one scope.')).toBeInTheDocument();
	});
});

describe('rotate', () => {
	it('sends the overlap, shows the new key once and when the old one stops', async () => {
		let sent: unknown;
		server.use(
			http.post(mswPath('/api-keys/:key_id/rotate'), async ({ request }) => {
				sent = await request.json();
				return HttpResponse.json(contractMocks.rotateApiKey.example);
			})
		);
		const user = userEvent.setup();
		const table = await renderPage();
		await user.click(within(table).getByRole('button', { name: 'Rotate hk_live_7Qm2' }));
		const dialog = await screen.findByRole('dialog', { name: 'Rotate API key hk_live_7Qm2' });
		expect(within(dialog).getByLabelText('Overlap')).toHaveValue('86400000');
		await user.type(within(dialog).getByLabelText(/Reason/), 'Scheduled rotation, OPS-202');
		await user.click(within(dialog).getByRole('button', { name: 'Rotate' }));
		const reveal = await screen.findByRole('dialog', { name: /Copy the replacement API key/ });
		expect(sent).toEqual({ overlap_ms: 86_400_000, reason: 'Scheduled rotation, OPS-202' });
		expect(
			within(reveal).getByLabelText(/New API key hk_live_Z9a1 \(replaces hk_live_7Qm2\)/)
		).toHaveValue(ROTATED);
		expect(reveal).toHaveTextContent(
			'The old key hk_live_7Qm2 keeps working until 2026-11-01 10:00 UTC (overlap 24 h)'
		);
		await user.click(within(reveal).getByLabelText('I have stored it somewhere safe'));
		await user.click(within(reveal).getByRole('button', { name: 'Done' }));
		expect(traces(ROTATED)).toEqual(NONE);
		expect(screen.getByText(/Rotated hk_live_7Qm2 to hk_live_Z9a1/)).toHaveTextContent(
			'until 2026-11-01 10:00 UTC'
		);
	});
});

describe('keys without a prefix (HUB-289)', () => {
	// Keys seeded from the config tree carry only a hash: the Hub answers prefix "". The table
	// showed an empty cell, "Revoke " buttons and a revoke dialog whose confirmation text was empty.
	const seeded = { ...contractMocks.listApiKeys.example.items[0]!, prefix: '' };

	it('names the key by its id and asks for the id before a revoke', async () => {
		let sent: unknown;
		server.use(
			mockOperation('listApiKeys', { items: [seeded], next_cursor: null }),
			http.post(mswPath('/api-keys/:key_id/revoke'), async ({ params }) => {
				sent = params.key_id;
				return HttpResponse.json(contractMocks.revokeApiKey.example);
			})
		);
		const user = userEvent.setup();
		const table = await renderPage();
		expect(within(table).getByRole('rowheader')).toHaveTextContent(seeded.key_id);
		await user.click(within(table).getByRole('button', { name: `Revoke ${seeded.key_id}` }));
		const dialog = await screen.findByRole('dialog', { name: `Revoke API key ${seeded.key_id}` });
		await user.type(within(dialog).getByLabelText(/Reason/), 'Seeded key no longer used');
		const confirm = within(dialog).getByRole('button', { name: 'Revoke' });
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByLabelText(/to confirm/), seeded.key_id);
		await user.click(confirm);
		await waitFor(() => expect(sent).toBe(seeded.key_id));
	});
});

describe('revoke', () => {
	async function openRevoke() {
		const user = userEvent.setup();
		const table = await renderPage();
		await user.click(within(table).getByRole('button', { name: 'Revoke hk_live_7Qm2' }));
		return {
			user,
			dialog: await screen.findByRole('dialog', { name: 'Revoke API key hk_live_7Qm2' })
		};
	}

	it('needs the prefix typed and a reason', async () => {
		let sent: unknown;
		server.use(
			http.post(mswPath('/api-keys/:key_id/revoke'), async ({ request, params }) => {
				sent = { key: params.key_id, body: await request.json() };
				return HttpResponse.json(contractMocks.revokeApiKey.example);
			})
		);
		const { user, dialog } = await openRevoke();
		expect(dialog).toHaveTextContent('cannot be undone');
		await user.type(within(dialog).getByLabelText(/Reason/), 'Key leaked in a CI log');
		const confirm = within(dialog).getByRole('button', { name: 'Revoke' });
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByLabelText(/to confirm/), 'hk_live_7Qm2');
		await user.click(confirm);
		expect(await screen.findByText(/Revoked API key hk_live_7Qm2/)).toBeInTheDocument();
		expect(sent).toEqual({
			key: 'key_01J9Z7F6G7H8J9K0M1N2P3Q4RS',
			body: { reason: 'Key leaked in a CI log' }
		});
	});

	it('409: already revoked', async () => {
		server.use(mockError('revokeApiKey', 409));
		const { user, dialog } = await openRevoke();
		await user.type(within(dialog).getByLabelText(/Reason/), 'Key leaked in a CI log');
		await user.type(within(dialog).getByLabelText(/to confirm/), 'hk_live_7Qm2');
		await user.click(within(dialog).getByRole('button', { name: 'Revoke' }));
		expect(await within(dialog).findByRole('alert')).toHaveTextContent(
			'Someone else changed API key hk_live_7Qm2'
		);
	});
});

describe('key actions', () => {
	const form = (fields: Record<string, string | string[]>) => {
		const f = new FormData();
		for (const [k, v] of Object.entries(fields)) for (const x of [v].flat()) f.append(k, x);
		return f;
	};

	it.each(['createKey', 'rotateKey'])(
		'%s refuses a plain (no-JS) post before calling the API',
		async (name) => {
			const calls: string[] = [];
			server.events.on('request:start', ({ request }) => calls.push(request.url));
			const r = await runAction(
				actions,
				name,
				form({
					reason: REASON,
					scope: 'invoke',
					key_id: 'key_01J9Z7F6G7H8J9K0M1N2P3Q4RS',
					overlap_ms: '0'
				}),
				{ params: { tenant_id: ID } }
			);
			expect(r).toMatchObject({ type: 'failure', status: 400 });
			expect(calls).toEqual([]);
		}
	);

	it('revoke re-checks the typed prefix', async () => {
		const r = await runAction(
			actions,
			'revokeKey',
			form({
				reason: REASON,
				key_id: 'key_01J9Z7F6G7H8J9K0M1N2P3Q4RS',
				prefix: 'hk_live_7Qm2',
				confirm: 'hk_live'
			}),
			{ params: { tenant_id: ID } }
		);
		expect(r).toMatchObject({ type: 'failure', data: { fields: { confirm: expect.any(String) } } });
	});

	it('rotate rejects an overlap over 30 days', async () => {
		const r = await runAction(
			actions,
			'rotateKey',
			form({ reason: REASON, key_id: 'key_01J9Z7F6G7H8J9K0M1N2P3Q4RS', overlap_ms: '2592000001' }),
			{ params: { tenant_id: ID }, enhanced: true }
		);
		expect(r).toMatchObject({
			type: 'failure',
			data: { fields: { overlap_ms: 'Pick an overlap.' } }
		});
	});

	it('create passes the secret through to the page once (and logs nothing)', async () => {
		const r = await runAction(actions, 'createKey', form({ reason: REASON, scope: 'invoke' }), {
			params: { tenant_id: ID },
			enhanced: true
		});
		expect(r).toMatchObject({
			type: 'success',
			data: { changeId: 'chg_01J9Z4B2C3D4E5F6G7H8J9K0NC', result: { secret: CREATED } }
		});
		expect(logged).toEqual([]);
	});
});

it('Escape does not close the reveal; Tab stays inside it', async () => {
	const { user, reveal } = await createKey();
	await user.keyboard('{Escape}');
	expect(reveal).toBeInTheDocument();
	for (let i = 0; i < 6; i++) await user.tab();
	await waitFor(() => expect(reveal.contains(document.activeElement)).toBe(true));
});

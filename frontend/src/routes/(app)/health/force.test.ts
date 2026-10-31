import { render, screen, within } from '@testing-library/svelte';
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
import { parseDuration } from './force';

// HUB-262: breaker force-open / force-close / clear, admin only, with reason and typed confirmation.

vi.mock('$app/navigation', () => ({ invalidate: vi.fn(), invalidateAll: vi.fn(), goto: vi.fn() }));

const REASON = 'Vendor maintenance window 10:00-10:30 IST';

beforeEach(() => useActions(actions));

async function renderBoard(access: Access = accessFromRoles(['admin'])) {
	const data = load({
		fetch,
		url: new URL('http://console.test/health'),
		depends: vi.fn()
	} as never) as object;
	render(Page, { props: { data: { access, ...data } as never } });
	await screen.findByRole('table');
	return userEvent.setup();
}

function capture(path: string, body: unknown) {
	const seen: unknown[] = [];
	server.use(
		http.post(mswPath(path), async ({ request }) => {
			seen.push(await request.json());
			return HttpResponse.json(body as never);
		})
	);
	return seen;
}

async function openDialog(user: ReturnType<typeof userEvent.setup>, button: string) {
	await user.click(screen.getByRole('button', { name: button }));
	return screen.findByRole('dialog');
}

describe('who sees the controls', () => {
	it.each<{ role: Role; visible: boolean }>([
		{ role: 'admin', visible: true },
		{ role: 'editor', visible: false },
		{ role: 'viewer', visible: false }
	])('$role: force controls visible=$visible', async ({ role, visible }) => {
		await renderBoard(accessFromRoles([role]));
		expect(!!screen.queryByRole('columnheader', { name: 'Force' })).toBe(visible);
		expect(!!screen.queryByRole('button', { name: 'Force open' })).toBe(visible);
		expect(!!screen.queryByRole('button', { name: 'Force closed' })).toBe(visible);
	});
});

describe('force open', () => {
	it('names the affected integrations, needs the typed account id and a reason', async () => {
		const seen = capture(
			'/breakers/:account_id/:endpoint_ref/force-open',
			contractMocks.forceOpenBreaker.example
		);
		const user = await renderBoard();
		const dialog = await openDialog(user, 'Force open');
		expect(dialog).toHaveAccessibleName('Force ct-brandA-in upload@1 open');
		expect(await within(dialog).findByText(/Affects 1 integration/)).toBeInTheDocument();
		expect(dialog).toHaveTextContent('int_evt_brandA');
		expect(within(dialog).getByLabelText('Hold for')).toHaveValue('1800000');
		const confirm = within(dialog).getByRole('button', { name: 'Force open' });
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByLabelText(/Type ct-brandA-in to confirm/), 'ct-brandA-in');
		expect(confirm).toBeEnabled();
		await user.click(confirm);
		expect(
			await screen.findByText('Forced open until 10:30 UTC: ct-brandA-in upload@1 is now open.')
		).toBeInTheDocument();
		expect(screen.getByText('chg_01J9Z4B2C3D4E5F6G7H8J9K0NQ')).toBeInTheDocument();
		expect(seen[0]).toEqual({ duration_ms: 1_800_000, reason: REASON });
	});

	it('"until someone clears it" sends duration_ms null', async () => {
		const seen = capture(
			'/breakers/:account_id/:endpoint_ref/force-open',
			contractMocks.forceOpenBreaker.example
		);
		const user = await renderBoard();
		const dialog = await openDialog(user, 'Force open');
		await user.selectOptions(within(dialog).getByLabelText('Hold for'), 'Until someone clears it');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.type(within(dialog).getByLabelText(/Type ct-brandA-in/), 'ct-brandA-in');
		await user.click(within(dialog).getByRole('button', { name: 'Force open' }));
		await screen.findByText(/Forced open/);
		expect(seen[0]).toEqual({ duration_ms: null, reason: REASON });
	});

	it.each([
		[403, 'FORBIDDEN'],
		[404, 'NOT_FOUND'],
		[400, 'VALIDATION_FAILED']
	])('%i keeps the dialog open with the error', async (status, code) => {
		server.use(mockError('forceOpenBreaker', status));
		const user = await renderBoard();
		const dialog = await openDialog(user, 'Force open');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.type(within(dialog).getByLabelText(/Type ct-brandA-in/), 'ct-brandA-in');
		await user.click(within(dialog).getByRole('button', { name: 'Force open' }));
		expect(await within(dialog).findByRole('alert')).toHaveTextContent(code);
	});

	it('warns when the affected integrations cannot be loaded', async () => {
		server.use(mockError('getBreaker', 404));
		const user = await renderBoard();
		const dialog = await openDialog(user, 'Force open');
		expect(
			await within(dialog).findByText(/Could not load the affected integrations/)
		).toHaveTextContent('NOT_FOUND');
	});
});

describe('force closed and clear', () => {
	it('force closed explains the drain of parked requests', async () => {
		const seen = capture(
			'/breakers/:account_id/:endpoint_ref/force-close',
			contractMocks.forceCloseBreaker.example
		);
		const user = await renderBoard();
		const dialog = await openDialog(user, 'Force closed');
		expect(dialog).toHaveTextContent('ramped drain of 1,290 parked requests');
		await user.type(
			within(dialog).getByLabelText(/Reason/),
			'Probe endpoint broken, traffic healthy'
		);
		await user.type(within(dialog).getByLabelText(/Type ct-brandA-in/), 'ct-brandA-in');
		await user.click(within(dialog).getByRole('button', { name: 'Force closed' }));
		expect(
			await screen.findByText('Forced closed until 10:10 UTC: ct-brandA-in upload@1 is now closed.')
		).toBeInTheDocument();
		expect(seen[0]).toMatchObject({ duration_ms: 1_800_000 });
	});

	it('clear force needs only a reason and reports automatic control', async () => {
		server.use(...presets['breaker-forced']!());
		const seen = capture(
			'/breakers/:account_id/:endpoint_ref/clear-force',
			contractMocks.clearBreakerForce.example
		);
		const user = await renderBoard();
		expect(screen.queryByRole('button', { name: 'Force open' })).toBeNull();
		const dialog = await openDialog(user, 'Clear force');
		expect(dialog).toHaveTextContent('now forced open');
		expect(within(dialog).queryByLabelText(/Type /)).toBeNull();
		await user.type(within(dialog).getByLabelText(/Reason/), 'Maintenance finished early');
		await user.click(within(dialog).getByRole('button', { name: 'Clear force' }));
		expect(
			await screen.findByText(
				'Force cleared: ct-brandA-in upload@1 is back under automatic control (half-open).'
			)
		).toBeInTheDocument();
		expect(seen[0]).toEqual({ reason: 'Maintenance finished early' });
	});

	it('clear force 409 (not forced any more) asks for a reload', async () => {
		server.use(...presets['breaker-forced']!(), mockError('clearBreakerForce', 409));
		const user = await renderBoard();
		const dialog = await openDialog(user, 'Clear force');
		await user.type(within(dialog).getByLabelText(/Reason/), 'Maintenance finished early');
		await user.click(within(dialog).getByRole('button', { name: 'Clear force' }));
		const alert = await within(dialog).findByRole('alert');
		expect(alert).toHaveTextContent('Someone else changed ct-brandA-in upload@1');
		expect(within(alert).getByRole('button', { name: 'Reload' })).toBeInTheDocument();
	});
});

describe('force actions', () => {
	const form = (fields: Record<string, string>) => {
		const f = new FormData();
		for (const [k, v] of Object.entries(fields)) f.set(k, v);
		return f;
	};
	const ok = { account_id: 'ct-brandA-in', endpoint_ref: 'upload@1', reason: REASON };

	it('refuses a wrong typed confirmation before calling the API', async () => {
		server.use(
			http.post(mswPath('/breakers/:account_id/:endpoint_ref/force-open'), () => {
				throw new Error('must not be called');
			})
		);
		const r = await runAction(
			actions,
			'forceOpen',
			form({ ...ok, confirm: 'ct-brandB', duration_ms: '' })
		);
		expect(r).toMatchObject({
			type: 'failure',
			status: 400,
			data: { fields: { confirm: 'Type ct-brandA-in to confirm.' } }
		});
	});

	it('refuses an out-of-range duration', async () => {
		const r = await runAction(
			actions,
			'forceClose',
			form({ ...ok, confirm: 'ct-brandA-in', duration_ms: '500' })
		);
		expect(r).toMatchObject({
			type: 'failure',
			status: 400,
			data: { fields: { duration_ms: expect.any(String) } }
		});
	});
});

describe('parseDuration', () => {
	it.each([
		['', { ok: true, value: null }],
		['1800000', { ok: true, value: 1_800_000 }],
		['86400000', { ok: true, value: 86_400_000 }],
		['999', { ok: false, message: 'Hold for between 1 second and 24 hours, or until cleared.' }],
		[
			'86400001',
			{ ok: false, message: 'Hold for between 1 second and 24 hours, or until cleared.' }
		],
		['soon', { ok: false, message: 'Pick how long to hold the breaker.' }]
	])('%j', (raw, want) => expect(parseDuration(raw)).toEqual(want));
});

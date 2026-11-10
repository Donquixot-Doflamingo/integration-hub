import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import type { Role } from '$lib/api/types';
import { applied, runAction, useActions } from '$lib/testing/forms';
import Page from './+page.svelte';
import { load } from './+page';
import { actions } from './+page.server';
import { DRY_RUN_REASON } from './bulk';

// HUB-267: replay and purge by filter: exact count from a dry run first, reason, typed count for
// purge, change log id after.

vi.mock('$app/navigation', () => ({ goto: vi.fn(), invalidateAll: vi.fn() }));

const REASON = 'Fixed T1 event-name sanitising (T1@8)';
const SEARCH = '?integration_id=int_evt_brandA&reason=UPSTREAM_REJECTED&replayed=false';
const FILTER = { integration_id: 'int_evt_brandA', reason: 'UPSTREAM_REJECTED', replayed: false };
const replayExample = contractMocks.replayDeadLetters.example;
const purgeExample = contractMocks.purgeDeadLetters.example;

beforeEach(() => useActions(actions));

async function renderAt(search = SEARCH, access: Access = accessFromRoles(['editor'])) {
	const data = load({ fetch, url: new URL(`http://console.test/dlq${search}`) } as never) as object;
	render(Page, { props: { data: { access, ...data } as never } });
	await screen.findByRole('table', { busy: false });
	return userEvent.setup();
}

/** Records request bodies; answers `dry` for dry runs and `real` otherwise. */
function capture(path: '/dlq/replay' | '/dlq/purge', dry: unknown, real: unknown) {
	const seen: Record<string, unknown>[] = [];
	server.use(
		http.post(mswPath(path), async ({ request }) => {
			const body = (await request.json()) as Record<string, unknown>;
			seen.push(body);
			return HttpResponse.json((body.dry_run ? dry : real) as never);
		})
	);
	return seen;
}

const dryOf = (r: typeof replayExample, matched = r.matched) => ({
	...r,
	change_id: null,
	dry_run: true,
	matched,
	affected: matched
});

describe('who sees the bulk actions', () => {
	it.each<{ role: Role; replay: boolean; purge: boolean }>([
		{ role: 'admin', replay: true, purge: true },
		{ role: 'editor', replay: true, purge: false },
		{ role: 'viewer', replay: false, purge: false },
		{ role: 'tenant_viewer', replay: false, purge: false }
	])('$role: replay=$replay purge=$purge', async ({ role, replay, purge }) => {
		await renderAt(
			SEARCH,
			accessFromRoles([role], role === 'tenant_viewer' ? ['orders-service'] : [])
		);
		expect(!!screen.queryByRole('button', { name: /Replay matching/ })).toBe(replay);
		expect(!!screen.queryByRole('button', { name: /Purge matching/ })).toBe(purge);
	});
});

describe('replay by filter', () => {
	it('counts first, shows the exact count, needs a reason, then reports the change', async () => {
		const seen = capture('/dlq/replay', dryOf(replayExample), replayExample);
		const user = await renderAt();
		await user.click(screen.getByRole('button', { name: /Replay matching/ }));
		const dialog = await screen.findByRole('dialog');
		// The dry run carried the URL filters; nothing was replayed yet.
		expect(seen).toEqual([{ filter: FILTER, dry_run: true, reason: DRY_RUN_REASON }]);
		expect(dialog).toHaveAccessibleName('Replay 42 dead letters');
		expect(dialog).toHaveTextContent('Moves the 42 dead letters matching these filters');
		expect(
			within(dialog).getByRole('list', { name: /Filters this replay uses/ })
		).toHaveTextContent('Reason UPSTREAM_REJECTED');
		const confirm = within(dialog).getByRole('button', { name: 'Replay' });
		expect(confirm).toBeDisabled();
		expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus();
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(confirm);
		const notice = (await screen.findByText(/^Replayed 42 dead letters/)).closest(
			'[role="status"]'
		) as HTMLElement;
		// Count (page), count again (server), replay.
		expect(seen.map((b) => b.dry_run)).toEqual([true, true, false]);
		expect(seen[2]).toEqual({ filter: FILTER, dry_run: false, reason: REASON });
		expect(notice).toHaveTextContent(replayExample.change_id!);
		expect(within(notice).getByRole('link', { name: 'Show the replayed entries' })).toHaveAttribute(
			'href',
			'/dlq?integration_id=int_evt_brandA&reason=UPSTREAM_REJECTED&replayed=true'
		);
		expect(screen.queryByRole('dialog')).toBeNull();
	});

	it('warns when the filters include entries already replayed', async () => {
		capture('/dlq/replay', dryOf(replayExample), replayExample);
		const user = await renderAt('?integration_id=int_evt_brandA');
		await user.click(screen.getByRole('button', { name: /Replay matching/ }));
		expect(await screen.findByRole('dialog')).toHaveTextContent(
			/include entries that were already replayed/
		);
	});

	it('says there is nothing to replay when the count is 0, without a dialog', async () => {
		capture('/dlq/replay', dryOf(replayExample, 0), replayExample);
		const user = await renderAt();
		await user.click(screen.getByRole('button', { name: /Replay matching/ }));
		expect(
			await screen.findByText('No dead letters match these filters, so there is nothing to replay.')
		).toBeInTheDocument();
		expect(screen.queryByRole('dialog')).toBeNull();
	});

	it('shows a failed count with code and request id', async () => {
		server.use(mockError('replayDeadLetters', 403));
		const user = await renderAt();
		await user.click(screen.getByRole('button', { name: /Replay matching/ }));
		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent('Could not count the dead letters to replay');
		expect(alert).toHaveTextContent('FORBIDDEN');
		expect(alert).toHaveTextContent(
			contractMocks.replayDeadLetters.errors['403']!.error.request_id
		);
	});

	it('keeps the dialog open with the error when the replay fails', async () => {
		let calls = 0;
		server.use(
			http.post(mswPath('/dlq/replay'), () =>
				calls++ === 0
					? HttpResponse.json(dryOf(replayExample) as never)
					: HttpResponse.json(contractMocks.replayDeadLetters.errors['400'] as never, {
							status: 400
						})
			)
		);
		const user = await renderAt();
		await user.click(screen.getByRole('button', { name: /Replay matching/ }));
		const dialog = await screen.findByRole('dialog');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Replay' }));
		expect(await within(dialog).findByRole('alert')).toHaveTextContent('VALIDATION_FAILED');
		expect(within(dialog).getByLabelText(/Reason/)).toHaveValue(REASON);
	});
});

describe('purge by filter (admin)', () => {
	it('needs the count typed, re-counts on the server, then purges', async () => {
		const seen = capture('/dlq/purge', dryOf(purgeExample), purgeExample);
		const user = await renderAt(SEARCH, accessFromRoles(['admin']));
		await user.click(screen.getByRole('button', { name: /Purge matching/ }));
		const dialog = await screen.findByRole('dialog');
		expect(dialog).toHaveAccessibleName('Purge 1 dead letter');
		expect(dialog).toHaveTextContent(
			'Permanently removes the 1 dead letter matching these filters'
		);
		const confirm = within(dialog).getByRole('button', { name: 'Purge' });
		await user.type(within(dialog).getByLabelText(/Reason/), 'Test traffic from staging client');
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByLabelText('Type 1 to confirm'), '1');
		expect(confirm).toBeEnabled();
		await user.click(confirm);
		expect(await screen.findByText(/^Purged 1 dead letter\./)).toBeInTheDocument();
		expect(screen.getByText(purgeExample.change_id!)).toBeInTheDocument();
		// Count (page), count again (server, before the permanent call), purge.
		expect(seen.map((b) => b.dry_run)).toEqual([true, true, false]);
		expect(seen[2]).toEqual({
			filter: FILTER,
			dry_run: false,
			reason: 'Test traffic from staging client'
		});
	});

	it('refuses to replay when the selection changed since it was counted (HUB-455)', async () => {
		let dry = 0;
		const seen: Record<string, unknown>[] = [];
		server.use(
			http.post(mswPath('/dlq/replay'), async ({ request }) => {
				const body = (await request.json()) as Record<string, unknown>;
				seen.push(body);
				return HttpResponse.json(dryOf(replayExample, dry++ === 0 ? 42 : 90) as never);
			})
		);
		const user = await renderAt();
		await user.click(screen.getByRole('button', { name: /Replay matching/ }));
		const dialog = await screen.findByRole('dialog');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Replay' }));
		expect(
			await within(dialog).findByText(/The selection changed: 90 now match, not 42/)
		).toBeInTheDocument();
		expect(seen.every((b) => b.dry_run === true)).toBe(true);
	});

	it('refuses to purge when the selection changed since it was counted', async () => {
		let dry = 0;
		const seen: Record<string, unknown>[] = [];
		server.use(
			http.post(mswPath('/dlq/purge'), async ({ request }) => {
				const body = (await request.json()) as Record<string, unknown>;
				seen.push(body);
				return HttpResponse.json(dryOf(purgeExample, dry++ === 0 ? 1 : 3) as never);
			})
		);
		const user = await renderAt(SEARCH, accessFromRoles(['admin']));
		await user.click(screen.getByRole('button', { name: /Purge matching/ }));
		const dialog = await screen.findByRole('dialog');
		await user.type(within(dialog).getByLabelText(/Reason/), 'Test traffic from staging client');
		await user.type(within(dialog).getByLabelText('Type 1 to confirm'), '1');
		await user.click(within(dialog).getByRole('button', { name: 'Purge' }));
		expect(
			await within(dialog).findByText(/The selection changed: 3 now match, not 1/)
		).toBeInTheDocument();
		expect(seen.every((b) => b.dry_run === true)).toBe(true);
	});
});

describe('bulk actions on the server', () => {
	const form = (fields: Record<string, string>) => {
		const f = new FormData();
		for (const [k, v] of Object.entries(fields)) f.set(k, v);
		return f;
	};
	const neverCalled = (path: string) =>
		server.use(
			http.post(mswPath(path), () => {
				throw new Error('must not be called');
			})
		);

	it('refuses a missing reason, a missing count and a wrong typed count before calling', async () => {
		neverCalled('/dlq/replay');
		neverCalled('/dlq/purge');
		expect(await runAction(actions, 'replay', form({ expected: '42' }))).toMatchObject({
			type: 'failure',
			status: 400,
			data: { fields: { reason: expect.any(String) } }
		});
		expect(await runAction(actions, 'replay', form({ reason: REASON }))).toMatchObject({
			type: 'failure',
			data: { fields: { reason: 'Count the matching dead letters first.' } }
		});
		expect(
			await runAction(actions, 'purge', form({ reason: REASON, expected: '1', confirm: '2' }))
		).toMatchObject({ type: 'failure', data: { fields: { confirm: 'Type 1 to confirm.' } } });
	});

	it('reads the filters from the prefixed fields, not the change reason', async () => {
		const seen = capture('/dlq/replay', dryOf(replayExample), replayExample);
		const r = await runAction(
			actions,
			'replay',
			form({ 'filter.account_id': 'ct-brandA-in', reason: REASON, expected: '42' })
		);
		expect(r).toMatchObject({
			type: 'success',
			data: { op: 'replay', changeId: replayExample.change_id }
		});
		expect(seen[1]).toEqual({
			filter: { account_id: 'ct-brandA-in' },
			dry_run: false,
			reason: REASON
		});
	});

	it('applies the sign-in redirect when the session expired during a count (HUB-455)', async () => {
		useActions(actions, { session: null });
		const user = await renderAt();
		await user.click(screen.getByRole('button', { name: /Replay matching/ }));
		await vi.waitFor(() => expect(applied).toHaveLength(1));
		expect(applied[0]).toMatchObject({ type: 'redirect', status: 303 });
		expect(screen.queryByText(/Try again/)).toBeNull();
	});

	it('sends the user back to sign in when the session expired', async () => {
		const r = await runAction(actions, 'countReplay', form({}), {
			session: null,
			url: 'http://console.test/dlq'
		});
		expect(r).toMatchObject({ type: 'redirect', status: 303 });
	});
});

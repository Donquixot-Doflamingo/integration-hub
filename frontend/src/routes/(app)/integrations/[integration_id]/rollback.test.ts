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

// HUB-259: rollback-as-new-version from the version history, with mandatory reason.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const REASON = 'Canary account returning UPSTREAM_REJECTED';
const ID = 'int_evt_brandA';

beforeEach(() => {
	// v8 active, v7 published: the contract's rollback example (to v7, expected v8 -> v9).
	server.use(...presets['v8-active']!());
	useActions(actions, { params: { integration_id: ID } });
});

async function renderDetail(access: Access = accessFromRoles(['editor'])) {
	const data = load({
		fetch,
		url: new URL(`http://console.test/integrations/${ID}`),
		params: { integration_id: ID }
	} as never) as object;
	render(Page, { props: { data: { access, ...data } as never } });
	return screen.findByRole('table', { name: /Versions of/, busy: false });
}

async function openRollback() {
	const user = userEvent.setup();
	const table = await renderDetail();
	await user.click(await within(table).findByRole('button', { name: 'Roll back to v7' }));
	const dialog = await screen.findByRole('dialog', { name: `Roll back ${ID} to v7` });
	return { user, dialog };
}

describe('rollback', () => {
	it('offers rollback only on published, non-active versions', async () => {
		const table = await renderDetail();
		await within(table).findByRole('button', { name: 'Roll back to v7' });
		expect(within(table).queryByRole('button', { name: 'Roll back to v8' })).toBeNull();
	});

	it('explains the new version, requires a reason and shows the new version and change id', async () => {
		let sent: unknown;
		server.use(
			http.post(mswPath('/integrations/:integration_id/rollback'), async ({ request }) => {
				sent = await request.json();
				return HttpResponse.json(contractMocks.rollbackIntegration.example);
			})
		);
		const { user, dialog } = await openRollback();
		expect(dialog).toHaveTextContent('Creates v9 with the content of v7');
		expect(dialog).toHaveTextContent('Nothing is deleted');
		expect(
			within(dialog).getByRole('link', { name: /Review what changes \(v8 to v7\)/ })
		).toHaveAttribute('href', `/integrations/${ID}/diff?from=8&to=7`);
		const confirm = within(dialog).getByRole('button', { name: 'Roll back' });
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(confirm);
		const notice = await screen.findByText(/Rolled back: v9 is now active, a copy of v7/);
		expect(notice.closest('[role="status"]')).toHaveTextContent('chg_01J9Z4B2C3D4E5F6G7H8J9K0N6');
		expect(sent).toEqual({ to_version: 7, expected_active_version: 8, reason: REASON });
	});

	it('409: someone else published first', async () => {
		server.use(mockError('rollbackIntegration', 409));
		const { user, dialog } = await openRollback();
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Roll back' }));
		const alert = await within(dialog).findByRole('alert');
		expect(alert).toHaveTextContent(`Someone else changed ${ID}`);
		expect(alert).toHaveTextContent('Active version is 8, expected 7');
		expect(within(alert).getByRole('button', { name: 'Reload' })).toBeInTheDocument();
	});

	it('403: shows the error with its request id and keeps the reason', async () => {
		server.use(mockError('rollbackIntegration', 403));
		const { user, dialog } = await openRollback();
		const reason = within(dialog).getByLabelText(/Reason/);
		await user.type(reason, REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Roll back' }));
		const alert = await within(dialog).findByRole('alert');
		expect(alert).toHaveTextContent('FORBIDDEN');
		expect(alert).toHaveTextContent(
			contractMocks.rollbackIntegration.errors['403']!.error.request_id
		);
		expect(reason).toHaveValue(REASON);
	});

	it('400: the reason pointer lands on the reason field', async () => {
		server.use(mockError('rollbackIntegration', 400));
		const { user, dialog } = await openRollback();
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Roll back' }));
		await waitFor(() =>
			expect(within(dialog).getByLabelText(/Reason/)).toHaveAccessibleDescription(
				/missing required property 'reason'/
			)
		);
	});

	it('422: shows the failing publish checks', async () => {
		server.use(mockError('rollbackIntegration', 422));
		const { user, dialog } = await openRollback();
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Roll back' }));
		const alert = await within(dialog).findByRole('alert');
		expect(alert).toHaveTextContent('PUBLISH_VALIDATION_FAILED');
		expect(alert).toHaveTextContent("account 'ct-brandB-us' is retired");
	});

	it.each<Role>(['viewer', 'tenant_viewer'])('%s sees no rollback control', async (role) => {
		const table = await renderDetail(accessFromRoles([role]));
		expect(within(table).queryByRole('button', { name: /Roll back/ })).toBeNull();
		expect(within(table).queryByRole('columnheader', { name: 'Actions' })).toBeNull();
		expect(screen.queryByRole('link', { name: /publish|new draft/i })).toBeNull();
	});
});

describe('rollback action', () => {
	const form = (fields: Record<string, string>) => {
		const f = new FormData();
		for (const [k, v] of Object.entries(fields)) f.set(k, v);
		return f;
	};

	it('rejects a missing or short reason without calling the API', async () => {
		server.use(
			http.post(mswPath('/integrations/:integration_id/rollback'), () => {
				throw new Error('must not be called');
			})
		);
		const r = await runAction(
			actions,
			'rollback',
			form({ to_version: '7', expected_active_version: '8', reason: 'short' }),
			{
				params: { integration_id: ID }
			}
		);
		expect(r).toMatchObject({
			type: 'failure',
			status: 400,
			data: { op: 'rollback', fields: { reason: expect.stringMatching(/at least 10/) } }
		});
	});

	it('rejects a missing target version', async () => {
		const r = await runAction(
			actions,
			'rollback',
			form({ expected_active_version: '8', reason: REASON }),
			{
				params: { integration_id: ID }
			}
		);
		expect(r).toMatchObject({
			type: 'failure',
			status: 400,
			data: { fields: { to_version: expect.any(String) } }
		});
	});

	it('sends a signed-out operator back to login, keeping the page', async () => {
		const r = await runAction(
			actions,
			'rollback',
			form({ to_version: '7', expected_active_version: '8', reason: REASON }),
			{
				params: { integration_id: ID },
				session: null,
				url: `http://console.test/integrations/${ID}`
			}
		);
		expect(r).toEqual({
			type: 'redirect',
			status: 303,
			location: `/login?next=%2Fintegrations%2F${ID}&expired=1`
		});
	});
});

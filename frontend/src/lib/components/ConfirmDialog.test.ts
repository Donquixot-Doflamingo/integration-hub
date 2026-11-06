import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { fail } from '@sveltejs/kit';
import { beforeEach, describe, expect, it, vi, type Mock } from 'vitest';
import { toPlainError } from '$lib/api/actions';
import { HubApiError } from '$lib/api/errors';
import { submissions, useActions } from '$lib/testing/forms';
import Harness from './ConfirmDialogHarness.test.svelte';

// HUB-259: the shared confirm dialog for dangerous actions (admin-console-ux §1).

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const REASON = 'v8 drops the campaign field';
const conflict = new HubApiError({
	status: 409,
	code: 'CONFLICT',
	message: 'Active version is 8, expected 7',
	requestId: 'req_01J9Z3M1A5B7C9D1E3F5G7H9KG'
});

let go: Mock<(event: never) => Promise<unknown>>;
beforeEach(() => {
	go = vi.fn(async (_event: never): Promise<unknown> => ({
		op: 'go',
		changeId: 'chg_01J9Z4B2C3D4E5F6G7H8J9K0N6',
		result: {}
	}));
	useActions({ go });
});

async function openDialog(props: Record<string, unknown> = {}) {
	const user = userEvent.setup();
	render(Harness, { props });
	const trigger = screen.getByRole('button', { name: 'Roll back to v7' });
	await user.click(trigger);
	const dialog = await screen.findByRole('dialog', { name: 'Roll back int_evt_brandA to v7' });
	return { user, trigger, dialog };
}

describe('ConfirmDialog', () => {
	it('states the consequence, focuses Cancel and blocks confirm until the reason is valid', async () => {
		const { user, dialog } = await openDialog();
		expect(dialog).toHaveAttribute('aria-modal', 'true');
		expect(dialog).toHaveAccessibleDescription('Creates v9 with the content of v7.');
		await waitFor(() =>
			expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus()
		);
		const confirm = within(dialog).getByRole('button', { name: 'Roll back' });
		expect(confirm).toBeDisabled();
		const reason = within(dialog).getByLabelText(/Reason/);
		expect(reason).toBeRequired();
		await user.type(reason, 'too short');
		expect(confirm).toBeDisabled();
		await user.tab();
		expect(within(dialog).getByText(/Use at least 10 characters \(9 so far\)/)).toBeInTheDocument();
		await user.clear(reason);
		await user.type(reason, `  ${REASON}  `);
		expect(confirm).toBeEnabled();
	});

	it('sends the trimmed reason and the fields, closes and reports the change id', async () => {
		const onsuccess = vi.fn();
		const { user, dialog, trigger } = await openDialog({ onsuccess });
		await user.type(within(dialog).getByLabelText(/Reason/), `  ${REASON}`);
		await user.click(within(dialog).getByRole('button', { name: 'Roll back' }));
		await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
		expect(onsuccess).toHaveBeenCalledWith(
			expect.objectContaining({ changeId: 'chg_01J9Z4B2C3D4E5F6G7H8J9K0N6' })
		);
		const [sent] = submissions();
		expect(sent?.action).toBe('go');
		expect(sent?.form.get('reason')).toBe(`  ${REASON}`);
		expect(sent?.form.get('to_version')).toBe('7');
		expect(trigger).toHaveFocus();
	});

	it('keeps the dialog open with the reason on an API error and shows code and request id', async () => {
		const error = new HubApiError({
			status: 403,
			code: 'FORBIDDEN',
			message: "Requires role 'editor'",
			requestId: 'req_01J9Z3M1A5B7C9D1E3F5G7H9KE'
		});
		go.mockResolvedValueOnce(fail(403, { op: 'go', error: toPlainError(error) }));
		const { user, dialog } = await openDialog();
		const reason = within(dialog).getByLabelText(/Reason/);
		await user.type(reason, REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Roll back' }));
		const alert = await within(dialog).findByRole('alert');
		expect(alert).toHaveTextContent('Could not roll back');
		expect(alert).toHaveTextContent('FORBIDDEN');
		expect(alert).toHaveTextContent('req_01J9Z3M1A5B7C9D1E3F5G7H9KE');
		expect(reason).toHaveValue(REASON);
		expect(within(dialog).getByRole('button', { name: 'Roll back' })).toBeEnabled();
	});

	it('maps a reason error from the action onto the field', async () => {
		go.mockResolvedValueOnce(fail(400, { op: 'go', fields: { reason: 'Reason is too vague' } }));
		const { user, dialog } = await openDialog();
		const reason = within(dialog).getByLabelText(/Reason/);
		await user.type(reason, REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Roll back' }));
		await within(dialog).findByText('Reason is too vague');
		expect(reason).toHaveAttribute('aria-invalid', 'true');
		expect(reason).toHaveAccessibleDescription(/Reason is too vague/);
	});

	it.each([
		['CONFLICT', 409],
		['PRECONDITION_FAILED', 412]
	])('%s: says someone else changed it and offers a reload', async (code, status) => {
		go.mockResolvedValueOnce(
			fail(status, {
				op: 'go',
				error: toPlainError(
					new HubApiError({ ...conflict, status, code, message: conflict.message })
				)
			})
		);
		const { user, dialog } = await openDialog();
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Roll back' }));
		const alert = await within(dialog).findByRole('alert');
		expect(alert).toHaveTextContent('Someone else changed int_evt_brandA');
		expect(alert).toHaveTextContent('Active version is 8, expected 7');
		expect(alert).toHaveTextContent(code);
		await user.click(within(alert).getByRole('button', { name: 'Reload' }));
		await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
	});

	it.each([
		['CONFLICT', 409, false],
		['PRECONDITION_FAILED', 412, true]
	])(
		'conflictIsStale=false, %s: a state conflict shows the API message, not a reload prompt',
		async (code, status, stale) => {
			// HUB-289: deleting a callback endpoint that is still subscribed answered "Someone else
			// changed reg_orders_cb ... Reload", which is wrong: nobody changed it, it is in use.
			const message = '2 integration version(s) subscribe to registrar endpoint reg_orders_cb';
			go.mockResolvedValueOnce(
				fail(status, {
					op: 'go',
					error: toPlainError(new HubApiError({ ...conflict, status, code, message }))
				})
			);
			const { user, dialog } = await openDialog({ conflictIsStale: false });
			await user.type(within(dialog).getByLabelText(/Reason/), REASON);
			await user.click(within(dialog).getByRole('button', { name: 'Roll back' }));
			const alert = await within(dialog).findByRole('alert');
			expect(alert).toHaveTextContent(code);
			if (stale) {
				expect(alert).toHaveTextContent('Someone else changed int_evt_brandA');
			} else {
				expect(alert).not.toHaveTextContent('Someone else changed');
				expect(alert).toHaveTextContent('Could not roll back');
				expect(alert).toHaveTextContent(message);
				expect(within(dialog).queryByRole('button', { name: 'Reload' })).toBeNull();
			}
		}
	);

	it('never submits twice while a request is in flight', async () => {
		let release!: () => void;
		go.mockImplementationOnce(
			() =>
				new Promise((r) => {
					release = () => r({ op: 'go', changeId: null, result: {} });
				})
		);
		const { user, dialog } = await openDialog();
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		const confirm = within(dialog).getByRole('button', { name: 'Roll back' });
		await user.click(confirm);
		const busy = await within(dialog).findByRole('button', { name: 'Rolling back...' });
		expect(busy).toBeDisabled();
		expect(within(dialog).getByRole('button', { name: 'Cancel' })).toBeDisabled();
		await user.click(busy);
		release();
		await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
		expect(go).toHaveBeenCalledTimes(1);
	});

	it('Escape closes and returns focus to the trigger; Tab stays inside', async () => {
		const { user, dialog, trigger } = await openDialog();
		const cancel = within(dialog).getByRole('button', { name: 'Cancel' });
		await waitFor(() => expect(cancel).toHaveFocus());
		const reason = within(dialog).getByLabelText(/Reason/);
		// Confirm is disabled, so the cycle is: link-free body -> reason -> cancel.
		await user.tab();
		expect(dialog.contains(document.activeElement)).toBe(true);
		await user.tab();
		await user.tab();
		expect(dialog.contains(document.activeElement)).toBe(true);
		await user.click(reason);
		await user.keyboard('{Escape}');
		expect(screen.queryByRole('dialog')).toBeNull();
		expect(trigger).toHaveFocus();
	});

	it('requires the typed confirmation when asked', async () => {
		const { user, dialog } = await openDialog({ confirmText: 'ct-brandA-in' });
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		const confirm = within(dialog).getByRole('button', { name: 'Roll back' });
		const typed = within(dialog).getByLabelText(/Type ct-brandA-in to confirm/);
		await user.type(typed, 'ct-brandA');
		expect(confirm).toBeDisabled();
		await user.type(typed, '-in');
		expect(confirm).toBeEnabled();
	});
});

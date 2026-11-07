import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mockOperation, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import { runAction, useActions } from '$lib/testing/forms';
import Page from './+page.svelte';
import { load } from './+page';
import { actions } from './+page.server';

// HUB-259: publish flow (spec editor, publish checks, diff, confirm with reason) against MSW.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const ID = 'int_evt_brandA';
const REASON = 'Canary 20% to the new workspace';
const integration = contractMocks.getIntegration.example;

beforeEach(() => useActions(actions, { params: { integration_id: ID } }));

async function renderPublish(access: Access = accessFromRoles(['editor'])) {
	const data = (await load({
		fetch,
		url: new URL(`http://console.test/integrations/${ID}/publish`),
		params: { integration_id: ID },
		parent: async () => ({ access })
	} as never)) as object;
	render(Page, { props: { data: { access, ...data } as never } });
	const user = userEvent.setup();
	// These tests drive the YAML editor (the form editor has its own tests, HUB-292).
	await user.click(await screen.findByRole('button', { name: 'YAML' }));
	const editor = await screen.findByLabelText(/^Spec/);
	return { user, editor: editor as HTMLTextAreaElement };
}

function captureBody(method: 'post' | 'put', path: string, body: unknown, status = 200) {
	const seen: unknown[] = [];
	server.use(
		http[method](mswPath(path), async ({ request }) => {
			seen.push(await request.json());
			return HttpResponse.json(body as never, { status });
		})
	);
	return seen;
}

describe('publish page', () => {
	it('loads the open draft (v8) into the editor and shows the diff against v7', async () => {
		const { editor } = await renderPublish();
		expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(`Publish ${ID}`);
		expect(screen.getByRole('region', { name: 'Draft' })).toHaveTextContent(
			'Draft v8, based on v7'
		);
		expect(editor.value).toContain('account_id: ct-brandA-in-v2');
		expect(editor.value).toContain('weight: 90');
		const diff = await screen.findByRole('region', { name: 'Changes against the active version' });
		expect(diff).toHaveTextContent('v7 to v8');
		expect(screen.getByRole('button', { name: 'Publish v8' })).toBeEnabled();
	});

	it('without a draft, starts from the active spec and creates the next version', async () => {
		server.use(
			mockOperation('getIntegration', { ...integration, has_draft: false, latest_version: 7 })
		);
		const seen = captureBody(
			'post',
			'/integrations/:integration_id/versions',
			contractMocks.createIntegrationDraft.example,
			201
		);
		const { user, editor } = await renderPublish();
		expect(screen.getByRole('region', { name: 'Draft' })).toHaveTextContent('No draft yet');
		expect(screen.getByRole('region', { name: 'Draft' })).toHaveTextContent('creates draft v8');
		expect(editor.value).toContain('weight: 100');
		// HUB-289: no draft yet, so no version number to show (was "Publish v?").
		expect(screen.getByRole('button', { name: 'Publish draft' })).toBeDisabled();
		await user.type(screen.getByLabelText(/Why this version/), REASON);
		await user.click(screen.getByRole('button', { name: 'Create draft' }));
		expect(
			await screen.findByText(/Saved draft v8\. All publish checks pass\./)
		).toBeInTheDocument();
		expect(seen[0]).toMatchObject({
			based_on: 7,
			reason: REASON,
			spec: { targets: [{ weight: 100 }] }
		});
		expect(screen.getByRole('region', { name: 'Publish checks' })).toHaveTextContent(
			'All 11 checks pass'
		);
	});

	it('saving needs a reason and valid YAML; syntax errors point at the line', async () => {
		const { user, editor } = await renderPublish();
		await user.clear(editor);
		await user.click(editor);
		await user.paste('targets: [ { vendor_id: a,\ndelivery: x');
		await user.type(screen.getByLabelText(/Why you are changing the draft/), REASON);
		await user.click(screen.getByRole('button', { name: 'Save draft v8' }));
		const problems = await screen.findByRole('alert');
		expect(problems).toHaveTextContent(/Line 2:/);
		expect(editor).toHaveAttribute('aria-invalid', 'true');
	});

	it('400 with /spec pointers lists them with their line and a "Go to line" button', async () => {
		server.use(
			mockError('updateIntegrationDraft', 400, {
				details: [
					{ pointer: '/spec/targets/1/weight', keyword: 'maximum', message: 'must be <= 100' },
					{
						pointer: '/reason',
						keyword: 'minLength',
						message: 'must NOT have fewer than 3 characters'
					}
				]
			})
		);
		const { user, editor } = await renderPublish();
		await user.type(editor, ' ');
		await user.type(screen.getByLabelText(/Why you are changing the draft/), REASON);
		await user.click(screen.getByRole('button', { name: 'Save draft v8' }));
		const lines = editor.value.split('\n');
		const weightLine = lines.findIndex((l) => l.includes('weight: 10') && !l.includes('100')) + 1;
		const goTo = await screen.findByRole('button', { name: /^Go to line/ });
		const item = goTo.closest('li')!;
		expect(item).toHaveTextContent('must be <= 100');
		expect(item).toHaveTextContent(`Line ${weightLine}`);
		expect(item).toHaveTextContent('targets[1].weight');
		await user.click(within(item).getByRole('button', { name: `Go to line ${weightLine}` }));
		expect(editor).toHaveFocus();
		expect(editor.value.slice(editor.selectionStart, editor.selectionEnd)).toContain('weight: 10');
		expect(screen.getByLabelText(/Why you are changing the draft/)).toHaveAccessibleDescription(
			/fewer than 3 characters/
		);
	});

	it('cannot publish unsaved text', async () => {
		const { user, editor } = await renderPublish();
		await user.type(editor, '# note');
		expect(screen.getByText('Unsaved changes')).toBeInTheDocument();
		const publish = screen.getByRole('button', { name: 'Publish v8' });
		expect(publish).toBeDisabled();
		expect(publish).toHaveAccessibleDescription(/Save your changes first/);
	});

	it('publishes with a reason and expected_active_version and shows the new active version', async () => {
		const seen = captureBody(
			'post',
			'/integrations/:integration_id/versions/:version/publish',
			contractMocks.publishIntegrationVersion.example
		);
		const { user } = await renderPublish();
		await user.click(screen.getByRole('button', { name: 'Publish v8' }));
		const dialog = await screen.findByRole('dialog', { name: `Publish ${ID} v8` });
		expect(dialog).toHaveTextContent('replacing v7');
		await waitFor(() => expect(dialog).toHaveTextContent('Changes:'));
		const confirm = within(dialog).getByRole('button', { name: 'Publish v8' });
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(confirm);
		expect(
			await screen.findByText(
				/Published v8\. It is now the active version \(was v7\), in snapshot 413\./
			)
		).toBeInTheDocument();
		expect(screen.getByText('chg_01J9Z4B2C3D4E5F6G7H8J9K0N5')).toBeInTheDocument();
		expect(seen[0]).toEqual({ expected_active_version: 7, reason: REASON });
	});

	it('422: failing checks show in the dialog and on the editor lines', async () => {
		server.use(mockError('publishIntegrationVersion', 422));
		const { user } = await renderPublish();
		await user.click(screen.getByRole('button', { name: 'Publish v8' }));
		const dialog = await screen.findByRole('dialog');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Publish v8' }));
		await within(dialog).findByRole('region', { name: 'Failing checks' });
		expect(within(dialog).getByRole('region', { name: 'Failing checks' })).toHaveTextContent(
			'2 of 2 checks fail'
		);
		const editorProblems = screen.getAllByText("account 'ct-brandB-us' is retired");
		expect(editorProblems.length).toBeGreaterThan(1);
	});

	it('409: someone else published first', async () => {
		server.use(mockError('publishIntegrationVersion', 409));
		const { user } = await renderPublish();
		await user.click(screen.getByRole('button', { name: 'Publish v8' }));
		const dialog = await screen.findByRole('dialog');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Publish v8' }));
		expect(await within(dialog).findByRole('alert')).toHaveTextContent(
			`Someone else changed ${ID}`
		);
	});

	it('403 on publish shows FORBIDDEN with the request id', async () => {
		server.use(mockError('publishIntegrationVersion', 403));
		const { user } = await renderPublish();
		await user.click(screen.getByRole('button', { name: 'Publish v8' }));
		const dialog = await screen.findByRole('dialog');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Publish v8' }));
		const alert = await within(dialog).findByRole('alert');
		expect(alert).toHaveTextContent('FORBIDDEN');
		expect(alert).toHaveTextContent(
			contractMocks.publishIntegrationVersion.errors['403']!.error.request_id
		);
	});

	it('409 when creating a draft that already exists asks for a reload', async () => {
		server.use(
			mockOperation('getIntegration', { ...integration, has_draft: false, latest_version: 7 }),
			mockError('createIntegrationDraft', 409)
		);
		const { user } = await renderPublish();
		await user.type(screen.getByLabelText(/Why this version/), REASON);
		await user.click(screen.getByRole('button', { name: 'Create draft' }));
		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent(`Someone else changed ${ID}`);
		expect(within(alert).getByRole('button', { name: 'Reload' })).toBeInTheDocument();
	});

	it('runs the publish checks on demand', async () => {
		server.use(
			mockOperation('validateIntegrationVersion', {
				valid: false,
				checks: [
					{
						check: 'weights_sum_100',
						status: 'fail',
						message: 'weights total 110',
						pointer: '/targets'
					}
				]
			})
		);
		const { user } = await renderPublish();
		await user.click(screen.getByRole('button', { name: 'Run publish checks on v8' }));
		const checks = await screen.findByRole('region', { name: 'Publish checks' });
		expect(checks).toHaveTextContent('1 of 1 checks fail');
		expect(checks).toHaveTextContent('Weights total 100');
		expect(checks).toHaveTextContent('Fail');
		expect(screen.getByRole('button', { name: 'Publish v8' })).toBeDisabled();
	});

	it('is read only for viewers', async () => {
		const data = (await load({
			fetch,
			url: new URL(`http://console.test/integrations/${ID}/publish`),
			params: { integration_id: ID },
			parent: async () => ({ access: accessFromRoles(['viewer']) })
		} as never)) as object;
		render(Page, { props: { data: { access: accessFromRoles(['viewer']), ...data } as never } });
		expect(
			screen.getByText(/Read only\. Editing and publishing versions needs the Editor role/)
		).toBeInTheDocument();
		expect(screen.queryByLabelText(/^Spec/)).toBeNull();
	});
});

describe('publish actions', () => {
	const form = (fields: Record<string, string>) => {
		const f = new FormData();
		for (const [k, v] of Object.entries(fields)) f.set(k, v);
		return f;
	};
	const opts = { params: { integration_id: ID } };

	it('first publish sends expected_active_version null', async () => {
		const seen = captureBody(
			'post',
			'/integrations/:integration_id/versions/:version/publish',
			contractMocks.publishIntegrationVersion.example
		);
		const r = await runAction(
			actions,
			'publish',
			form({ version: '1', expected_active_version: '', reason: REASON }),
			opts
		);
		expect(r.type).toBe('success');
		expect(seen[0]).toEqual({ expected_active_version: null, reason: REASON });
	});

	it('publish refuses without a reason, before calling the API', async () => {
		const r = await runAction(
			actions,
			'publish',
			form({ version: '8', expected_active_version: '7' }),
			opts
		);
		expect(r).toMatchObject({
			type: 'failure',
			status: 400,
			data: { fields: { reason: expect.any(String) } }
		});
	});

	it('saveDraft replaces an existing draft (PUT) and runs the checks', async () => {
		const seen = captureBody(
			'put',
			'/integrations/:integration_id/versions/:version',
			contractMocks.updateIntegrationDraft.example
		);
		const r = await runAction(
			actions,
			'saveDraft',
			form({
				version: '8',
				spec: 'targets: []\ndelivery: { mode: wait }\naudit: { payloads: masked }',
				reason: REASON
			}),
			opts
		);
		expect(r).toMatchObject({
			type: 'success',
			data: {
				op: 'saveDraft',
				changeId: 'chg_01J9Z4B2C3D4E5F6G7H8J9K0N4',
				result: { version: 8, report: { valid: true } }
			}
		});
		expect(seen[0]).toEqual({
			spec: { targets: [], delivery: { mode: 'wait' }, audit: { payloads: 'masked' } },
			reason: REASON
		});
	});
});

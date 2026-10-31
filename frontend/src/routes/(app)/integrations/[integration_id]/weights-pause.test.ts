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

// HUB-260: target weights (must total 100) and pause / resume, each with a mandatory reason.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const ID = 'int_evt_brandA';
const REASON = 'Canary healthy for 24 h, go 50/50';

beforeEach(() => useActions(actions, { params: { integration_id: ID } }));

async function renderDetail(access: Access = accessFromRoles(['editor'])) {
	const data = load({
		fetch,
		url: new URL(`http://console.test/integrations/${ID}`),
		params: { integration_id: ID }
	} as never) as object;
	render(Page, { props: { data: { access, ...data } as never } });
	await screen.findByRole('region', { name: 'Summary' });
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

describe('weights', () => {
	beforeEach(() => server.use(...presets['v8-active']!()));

	async function openWeights() {
		const user = await renderDetail();
		await user.click(screen.getByRole('button', { name: 'Set weights' }));
		const dialog = await screen.findByRole('dialog', { name: `Set weights of ${ID}` });
		const a = within(dialog).getByLabelText('clevertap/ct-brandA-in');
		const b = within(dialog).getByLabelText('clevertap/ct-brandA-in-v2');
		return { user, dialog, a, b };
	}

	it('starts from the current weights and blocks totals other than 100', async () => {
		const { user, dialog, a, b } = await openWeights();
		expect(a).toHaveValue(90);
		expect(b).toHaveValue(10);
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		const confirm = within(dialog).getByRole('button', { name: 'Publish v9' });
		// Unchanged weights: nothing to do.
		expect(confirm).toBeDisabled();
		expect(dialog).toHaveTextContent('Change at least one weight');
		await user.clear(a);
		await user.type(a, '60');
		await user.clear(b);
		await user.type(b, '50');
		expect(within(dialog).getByRole('status')).toHaveTextContent('Total 110%: must be 100');
		expect(confirm).toBeDisabled();
		await user.clear(b);
		await user.type(b, '40');
		expect(within(dialog).getByRole('group', { name: 'Weights (%)' })).toHaveAccessibleDescription(
			'Total 100%'
		);
		expect(confirm).toBeEnabled();
	});

	it('publishes the new weights with expected_active_version and shows the new version', async () => {
		const seen = capture(
			'/integrations/:integration_id/weights',
			contractMocks.updateIntegrationWeights.example
		);
		const { user, dialog, a, b } = await openWeights();
		await user.clear(a);
		await user.type(a, '50');
		await user.clear(b);
		await user.type(b, '50');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Publish v9' }));
		expect(await screen.findByText(/Weights set: v9 is now active/)).toBeInTheDocument();
		expect(screen.getByText('chg_01J9Z4B2C3D4E5F6G7H8J9K0N9')).toBeInTheDocument();
		expect(seen[0]).toEqual({
			expected_active_version: 8,
			weights: [
				{ vendor_id: 'clevertap', account_id: 'ct-brandA-in', weight: 50 },
				{ vendor_id: 'clevertap', account_id: 'ct-brandA-in-v2', weight: 50 }
			],
			publish: true,
			reason: REASON
		});
	});

	it('can leave the new version as a draft', async () => {
		const draftResult = {
			...contractMocks.updateIntegrationWeights.example,
			active_version: 8,
			previous_active_version: 8,
			created_version: 9,
			published_at: null,
			snapshot_version: null
		};
		const seen = capture('/integrations/:integration_id/weights', draftResult);
		const { user, dialog, a, b } = await openWeights();
		await user.clear(a);
		await user.type(a, '80');
		await user.clear(b);
		await user.type(b, '20');
		await user.click(within(dialog).getByRole('checkbox', { name: 'Publish v9 now' }));
		expect(dialog).toHaveTextContent('as a draft. v8 stays active');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Create draft v9' }));
		expect(
			await screen.findByText(/Weights saved as draft v9\. v8 stays active/)
		).toBeInTheDocument();
		expect(seen[0]).toMatchObject({ publish: false });
	});

	it.each([
		[409, /Someone else changed int_evt_brandA/],
		[403, /FORBIDDEN/],
		[422, /PUBLISH_VALIDATION_FAILED/]
	])('%i is shown in the dialog', async (status, text) => {
		server.use(mockError('updateIntegrationWeights', status));
		const { user, dialog, a, b } = await openWeights();
		await user.clear(a);
		await user.type(a, '50');
		await user.clear(b);
		await user.type(b, '50');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Publish v9' }));
		expect(await within(dialog).findByRole('alert')).toHaveTextContent(text);
	});

	it('400 with a /weights pointer is shown on the weights', async () => {
		server.use(
			mockError('updateIntegrationWeights', 400, {
				details: [{ pointer: '/weights/1/weight', keyword: 'maximum', message: 'must be <= 100' }]
			})
		);
		const { user, dialog, a, b } = await openWeights();
		await user.clear(a);
		await user.type(a, '50');
		await user.clear(b);
		await user.type(b, '50');
		await user.type(within(dialog).getByLabelText(/Reason/), REASON);
		await user.click(within(dialog).getByRole('button', { name: 'Publish v9' }));
		expect(await within(dialog).findAllByText('must be <= 100')).not.toHaveLength(0);
	});

	it('is not offered with a single target (its weight can only be 100)', async () => {
		server.resetHandlers();
		await renderDetail();
		expect(screen.queryByRole('button', { name: 'Set weights' })).toBeNull();
	});
});

describe('pause and resume', () => {
	it('pause states the impact, needs a reason and reports the change id', async () => {
		const seen = capture(
			'/integrations/:integration_id/pause',
			contractMocks.pauseIntegration.example
		);
		const user = await renderDetail();
		await user.click(screen.getByRole('button', { name: 'Pause integration' }));
		const dialog = await screen.findByRole('dialog', { name: `Pause ${ID}` });
		expect(dialog).toHaveTextContent('503 INTEGRATION_PAUSED');
		expect(dialog).toHaveTextContent('workers hold queued requests');
		expect(dialog).toHaveTextContent('Other integrations on the same accounts are not affected');
		const confirm = within(dialog).getByRole('button', { name: 'Pause' });
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByLabelText(/Reason/), 'Vendor reports data corruption');
		await user.click(confirm);
		expect(await screen.findByText(/Paused int_evt_brandA\./)).toBeInTheDocument();
		expect(screen.getByText('chg_01J9Z4B2C3D4E5F6G7H8J9K0N7')).toBeInTheDocument();
		expect(seen[0]).toEqual({ reason: 'Vendor reports data corruption' });
	});

	it('resume is offered when paused', async () => {
		server.use(...presets.paused!());
		const seen = capture(
			'/integrations/:integration_id/resume',
			contractMocks.resumeIntegration.example
		);
		const user = await renderDetail();
		expect(screen.queryByRole('button', { name: 'Pause integration' })).toBeNull();
		await user.click(screen.getByRole('button', { name: 'Resume integration' }));
		const dialog = await screen.findByRole('dialog', { name: `Resume ${ID}` });
		expect(dialog).toHaveTextContent('Vendor reports data corruption on brand A workspace');
		await user.type(within(dialog).getByLabelText(/Reason/), 'Vendor confirmed fix');
		await user.click(within(dialog).getByRole('button', { name: 'Resume' }));
		expect(await screen.findByText(/Resumed int_evt_brandA\./)).toBeInTheDocument();
		expect(seen[0]).toEqual({ reason: 'Vendor confirmed fix' });
	});

	it.each([
		['pauseIntegration', 403, /FORBIDDEN/],
		['pauseIntegration', 404, /NOT_FOUND/],
		['pauseIntegration', 400, /missing required property 'reason'/]
	] as const)('%s %i keeps the dialog open', async (op, status, text) => {
		server.use(mockError(op, status));
		const user = await renderDetail();
		await user.click(screen.getByRole('button', { name: 'Pause integration' }));
		const dialog = await screen.findByRole('dialog');
		await user.type(within(dialog).getByLabelText(/Reason/), 'Vendor reports data corruption');
		await user.click(within(dialog).getByRole('button', { name: 'Pause' }));
		if (status === 400) {
			expect(await within(dialog).findAllByText(text)).not.toHaveLength(0);
			expect(within(dialog).getByLabelText(/Reason/)).toHaveAttribute('aria-invalid', 'true');
		} else {
			expect(await within(dialog).findByRole('alert')).toHaveTextContent(text);
		}
	});

	it('resume errors are shown too', async () => {
		server.use(...presets.paused!(), mockError('resumeIntegration', 403));
		const user = await renderDetail();
		await user.click(screen.getByRole('button', { name: 'Resume integration' }));
		const dialog = await screen.findByRole('dialog');
		await user.type(within(dialog).getByLabelText(/Reason/), 'Vendor confirmed fix');
		await user.click(within(dialog).getByRole('button', { name: 'Resume' }));
		expect(await within(dialog).findByRole('alert')).toHaveTextContent('FORBIDDEN');
	});

	it.each<Role>(['viewer', 'tenant_viewer'])(
		'%s gets no weights or pause control',
		async (role) => {
			server.use(...presets['v8-active']!());
			await renderDetail(accessFromRoles([role]));
			expect(screen.queryByRole('button', { name: /Set weights|Pause|Resume/ })).toBeNull();
		}
	);
});

describe('weights and pause actions', () => {
	const form = (fields: Record<string, string>) => {
		const f = new FormData();
		for (const [k, v] of Object.entries(fields)) f.set(k, v);
		return f;
	};
	const opts = { params: { integration_id: ID } };

	it('weights not totalling 100 are refused before calling the API', async () => {
		server.use(
			http.post(mswPath('/integrations/:integration_id/weights'), () => {
				throw new Error('must not be called');
			})
		);
		const r = await runAction(
			actions,
			'weights',
			form({
				reason: REASON,
				expected_active_version: '8',
				targets: '2',
				vendor_0: 'v',
				account_0: 'a',
				weight_0: '70',
				vendor_1: 'v',
				account_1: 'b',
				weight_1: '40'
			}),
			opts
		);
		expect(r).toMatchObject({
			type: 'failure',
			status: 400,
			data: { op: 'weights', fields: { weights: 'Weights must total 100 (now 110).' } }
		});
	});

	it('pause refuses an empty reason', async () => {
		const r = await runAction(actions, 'pause', form({ reason: '   ' }), opts);
		expect(r).toMatchObject({
			type: 'failure',
			status: 400,
			data: { fields: { reason: expect.stringMatching(/Enter a reason/) } }
		});
	});
});

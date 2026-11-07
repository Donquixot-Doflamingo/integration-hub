import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockOperation, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import type { IntegrationSpec } from '$lib/api/types';
import { accessFromRoles } from '$lib/auth/roles';
import { AADHAAR } from '$lib/testing/aadhaar';
import { useActions } from '$lib/testing/forms';
import Page from './+page.svelte';
import { load } from './+page';
import { actions } from './+page.server';

// HUB-436 (ADR-0023): the Operations section of the draft form: per operation a name,
// description, idempotent, the validate_input switch and the caller's request schema (body
// required, headers optional) built with the schema editor.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const ID = 'int_age_estimate';
const editor = accessFromRoles(['editor']);
const REASON = 'First version of the age estimate';

function withDraft(spec: IntegrationSpec | null) {
	const integration = {
		...contractMocks.getIntegration.example,
		integration_id: ID,
		interactive: false,
		active_version: null,
		active: null,
		latest_version: spec ? 1 : null,
		has_draft: !!spec
	};
	server.use(mockOperation('getIntegration', integration));
	if (spec)
		server.use(
			mockOperation('getIntegrationVersion', {
				...contractMocks.createIntegrationDraft.example.integration_version,
				integration_id: ID,
				version: 1,
				based_on: null,
				spec
			})
		);
}

/** Records the spec of createIntegrationDraft / updateIntegrationDraft. */
function captureSpecs() {
	const specs: IntegrationSpec[] = [];
	const answer = async ({ request }: { request: Request }) => {
		specs.push(((await request.json()) as { spec: IntegrationSpec }).spec);
		return HttpResponse.json(
			{
				...contractMocks.createIntegrationDraft.example,
				integration_version: {
					...contractMocks.createIntegrationDraft.example.integration_version,
					version: 1
				}
			},
			{ status: 201 }
		);
	};
	server.use(
		http.post(mswPath('/integrations/:integration_id/versions'), answer),
		http.put(mswPath('/integrations/:integration_id/versions/:version'), answer)
	);
	return specs;
}

async function renderPage() {
	const data = (await load({
		fetch,
		url: new URL(`http://console.test/integrations/${ID}/publish`),
		params: { integration_id: ID },
		parent: async () => ({ access: editor })
	} as never)) as object;
	render(Page, { props: { data: { access: editor, ...data } as never } });
	const ops = await screen.findByRole('group', { name: 'Operations' });
	return { user: userEvent.setup(), ops };
}

const opItem = (name: string) =>
	screen.getByRole('listitem', { name: new RegExp(`^Operation ${name}$`) });

beforeEach(() => useActions(actions, { params: { integration_id: ID } }));

describe('operations editor', () => {
	it('shows each operation with its request schema and the validate_input switch', async () => {
		withDraft(AADHAAR);
		await renderPage();
		const send = opItem('send_otp');
		expect(within(send).getByLabelText(/^Description \(shown to callers\)/)).toHaveValue(
			'Send an OTP to the mobile number linked to the Aadhaar number.'
		);
		expect(within(send).getByRole('checkbox', { name: /^Idempotent/ })).not.toBeChecked();
		const sw = within(send).getByRole('switch', { name: /Check live calls/ });
		expect(sw).toBeChecked();
		expect(sw).toHaveAccessibleName(/On$/);
		expect(sw).toHaveAccessibleDescription(/400 VALIDATION_FAILED/);
		const body = within(send).getByRole('group', { name: /^Request body/ });
		expect(within(body).getByRole('checkbox', { name: 'aadhaar_number required' })).toBeChecked();
		expect(within(body).getByText(/x-pii government_id/)).toBeInTheDocument();
		expect(within(send).getByRole('group', { name: /^Request headers/ })).toHaveTextContent(
			'Nothing declared: not sent.'
		);
	});

	it('a first draft builds the body from a sample, marks fields optional and saves it', async () => {
		withDraft(null);
		const specs = captureSpecs();
		const { user } = await renderPage();
		const op = opItem('default');
		await user.type(
			within(op).getByLabelText(/^Description \(shown to callers\)/),
			'Estimate the age.'
		);
		await user.click(within(op).getByRole('checkbox', { name: /^Idempotent/ }));
		const body = within(op).getByRole('group', { name: /^Request body/ });
		await user.click(within(body).getByRole('button', { name: 'Paste a sample' }));
		await fireEvent.input(within(body).getByLabelText('Sample request body (JSON)'), {
			target: { value: '{"firstName": "Mich", "lastName": "ael", "nickname": "M"}' }
		});
		await user.click(within(body).getByRole('button', { name: 'Generate fields' }));
		await user.click(within(body).getByRole('checkbox', { name: 'nickname required' }));
		const headers = within(op).getByRole('group', { name: /^Request headers/ });
		await user.click(within(headers).getByRole('button', { name: '+ Add header' }));
		const hn = within(headers).getByLabelText('Name of header');
		await user.clear(hn);
		await user.type(hn, 'X-Request-Source');
		await user.click(within(op).getByRole('switch', { name: /Check live calls/ }));
		expect(within(op).getByRole('switch', { name: /Check live calls/ })).toHaveAccessibleName(
			/Off$/
		);

		// A target, so the console's own checks pass.
		await user.click(screen.getByRole('button', { name: 'Add target' }));
		const t = screen.getByRole('listitem', { name: /^Target 1/ });
		await user.type(within(t).getByLabelText('Vendor'), 'agify');
		await user.type(within(t).getByLabelText('Account'), 'agify-public');
		await user.type(within(t).getByLabelText('Request transformer'), 'AGE_REQ@1');
		await user.type(screen.getByLabelText(/^Why this version/), REASON);
		await user.click(screen.getByRole('button', { name: 'Create draft' }));
		await waitFor(() => expect(specs).toHaveLength(1));
		expect(specs[0]!.operations).toEqual({
			default: {
				description: 'Estimate the age.',
				idempotent: true,
				validate_input: false,
				request_schema: {
					body: {
						type: 'object',
						required: ['firstName', 'lastName'],
						properties: {
							firstName: { type: 'string' },
							lastName: { type: 'string' },
							nickname: { type: 'string' }
						}
					},
					headers: { type: 'object', properties: { 'X-Request-Source': { type: 'string' } } }
				}
			}
		});
		// No response transformer: pass-through.
		expect(specs[0]!.targets[0]!.transformer).toEqual({ request: 'AGE_REQ@1', response: null });
	});

	it('renames an operation and moves the targets pairs with it', async () => {
		withDraft(AADHAAR);
		const specs = captureSpecs();
		const { user } = await renderPage();
		const name = within(opItem('verify_otp')).getByLabelText('Name');
		await user.clear(name);
		await user.type(name, 'Bad Name{Enter}');
		expect(name).toHaveAccessibleDescription(/Lower-case letters/);
		await user.clear(name);
		await user.type(name, 'send_otp{Enter}');
		expect(name).toHaveAccessibleDescription(/"send_otp" exists already/);
		await user.clear(name);
		await user.type(name, 'verify{Enter}');
		expect(opItem('verify')).toBeInTheDocument();
		const target = screen.getByRole('listitem', { name: /^Target 1/ });
		expect(within(target).getByLabelText('Request transformer for verify')).toHaveValue(
			'CF-OKYC-VERIFY-REQ@1'
		);
		await user.type(screen.getByLabelText(/^Why you are changing the draft/), 'Shorter name');
		await user.click(screen.getByRole('button', { name: 'Save draft v1' }));
		await waitFor(() => expect(specs).toHaveLength(1));
		expect(Object.keys(specs[0]!.operations)).toEqual(['send_otp', 'verify']);
		expect(Object.keys(specs[0]!.targets[0]!.operations!)).toEqual(['verify']);
	});

	it('adds and removes operations; the last one cannot go', async () => {
		withDraft(AADHAAR);
		const { user, ops } = await renderPage();
		await user.click(within(ops).getByRole('button', { name: 'Add operation' }));
		expect(opItem('operation_2')).toBeInTheDocument();
		const target = screen.getByRole('listitem', { name: /^Target 1/ });
		expect(within(target).getByRole('listitem', { name: 'operation_2' })).toHaveTextContent(
			'Uses the default pair.'
		);
		await user.click(screen.getByRole('button', { name: 'Remove operation verify_otp' }));
		await user.click(screen.getByRole('button', { name: 'Remove operation operation_2' }));
		expect(within(target).queryByRole('listitem', { name: 'verify_otp' })).toBeNull();
		expect(screen.getByRole('button', { name: 'Remove operation send_otp' })).toBeDisabled();
	});

	it('an input_schema check shows on the operation body with its sample and field', async () => {
		withDraft(AADHAAR);
		server.use(
			mockOperation('validateIntegrationVersion', {
				valid: false,
				checks: [
					{
						check: 'input_schema',
						status: 'fail',
						message: "sample 'otp_sent': /body/aadhaar_number must match pattern",
						pointer: '/operations/send_otp/request_schema/body',
						sample: 'otp_sent',
						field: '/body/aadhaar_number'
					}
				]
			})
		);
		const { user } = await renderPage();
		await user.click(screen.getByRole('button', { name: 'Run publish checks on v1' }));
		const body = within(opItem('send_otp')).getByRole('group', { name: /^Request body/ });
		expect(
			await within(body).findByText(/otp_sent.*aadhaar_number must match pattern/)
		).toBeInTheDocument();
		const checks = screen.getByRole('region', { name: 'Publish checks' });
		expect(checks).toHaveTextContent('Sample inputs fit the request schema');
		expect(checks).toHaveTextContent('sample otp_sent');
		expect(checks).toHaveTextContent('field /body/aadhaar_number');
	});
});

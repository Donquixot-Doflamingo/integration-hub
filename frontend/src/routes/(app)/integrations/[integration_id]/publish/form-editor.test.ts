import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mockOperation, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import type { IntegrationSpec } from '$lib/api/types';
import { accessFromRoles } from '$lib/auth/roles';
import { AADHAAR } from '$lib/testing/aadhaar';
import { useActions } from '$lib/testing/forms';
import { parseSpecYaml } from '$lib/utils/spec-yaml';
import Page from './+page.svelte';
import { load } from './+page';
import { actions } from './+page.server';

// HUB-292: the structured draft editor on the publish page. The Aadhaar example (one target,
// send_otp on the default pair, verify_otp on its own) as the open draft; capability operations,
// vendors, accounts and transformers from the lookups.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const ID = 'int_aadhaar_okyc';
const editor = accessFromRoles(['editor']);

function aadhaarDraft(spec: IntegrationSpec = AADHAAR) {
	const integration = {
		...contractMocks.getIntegration.example,
		integration_id: ID,
		capability: 'demo.aadhaar_okyc@v1',
		active_version: null,
		active: null,
		latest_version: 1,
		has_draft: true
	};
	const draft = {
		...contractMocks.createIntegrationDraft.example.integration_version,
		integration_id: ID,
		version: 1,
		based_on: null,
		spec
	};
	server.use(
		mockOperation('getIntegration', integration),
		mockOperation('getIntegrationVersion', draft),
		mockOperation('getCapability', {
			...contractMocks.getCapability.example,
			capability: 'demo.aadhaar_okyc@v1',
			interactive: true,
			operations: {
				send_otp: contractMocks.getCapability.example.operations.default!,
				verify_otp: contractMocks.getCapability.example.operations.default!
			}
		})
	);
}

/** Captures the spec of `updateIntegrationDraft` and answers the contract example. */
function captureSave() {
	const specs: IntegrationSpec[] = [];
	server.use(
		http.put(mswPath('/integrations/:integration_id/versions/:version'), async ({ request }) => {
			specs.push(((await request.json()) as { spec: IntegrationSpec }).spec);
			return HttpResponse.json({
				...contractMocks.updateIntegrationDraft.example,
				integration_version: {
					...contractMocks.updateIntegrationDraft.example.integration_version,
					version: 1
				}
			});
		})
	);
	return specs;
}

async function renderEditor() {
	const data = (await load({
		fetch,
		url: new URL(`http://console.test/integrations/${ID}/publish`),
		params: { integration_id: ID },
		parent: async () => ({ access: editor })
	} as never)) as object;
	render(Page, { props: { data: { access: editor, ...data } as never } });
	const target = await screen.findByRole('listitem', { name: /^Target 1/ });
	// The capability's operations arrive with the lookups (send_otp is not in the spec).
	await within(target).findByRole('listitem', { name: 'send_otp' });
	return { user: userEvent.setup(), target };
}

beforeEach(() => useActions(actions, { params: { integration_id: ID } }));

describe('form editor', () => {
	it('shows int_aadhaar_okyc: default pair, send_otp on it, verify_otp on its own pair', async () => {
		aadhaarDraft();
		const { target } = await renderEditor();
		expect(screen.getByRole('button', { name: 'Form' })).toHaveAttribute('aria-pressed', 'true');
		expect(within(target).getByLabelText('Vendor')).toHaveValue('cashfree-sid');
		expect(within(target).getByLabelText('Request transformer')).toHaveValue('CF-OKYC-OTP-REQ@1');
		const send = within(target).getByRole('listitem', { name: 'send_otp' });
		expect(within(send).getByRole('checkbox')).not.toBeChecked();
		expect(send).toHaveTextContent('Uses the default pair.');
		const verify = within(target).getByRole('listitem', { name: 'verify_otp' });
		expect(within(verify).getByRole('checkbox')).toBeChecked();
		expect(within(verify).getByLabelText('Request transformer for verify_otp')).toHaveValue(
			'CF-OKYC-VERIFY-REQ@1'
		);
		expect(screen.getByLabelText('Mode')).toHaveValue('sync');
		expect(screen.getByLabelText('Payloads in the audit trail')).toHaveValue('sealed');
	});

	it('saves what the form shows, per-operation pairs included', async () => {
		aadhaarDraft();
		const specs = captureSave();
		const { user, target } = await renderEditor();
		const send = within(target).getByRole('listitem', { name: 'send_otp' });
		await user.click(within(send).getByRole('checkbox', { name: 'Own transformers for send_otp' }));
		const req = within(send).getByLabelText('Request transformer for send_otp');
		expect(req).toHaveValue('CF-OKYC-OTP-REQ@1');
		await user.clear(req);
		await user.type(req, 'CF-OKYC-OTP-REQ@2');
		await user.type(
			screen.getByLabelText(/^Why you are changing the draft/),
			'send_otp on v2 of the OTP mapping'
		);
		await user.click(screen.getByRole('button', { name: 'Save draft v1' }));
		await waitFor(() => expect(specs).toHaveLength(1));
		expect(specs[0]!.targets[0]!.operations).toEqual({
			verify_otp: { request: 'CF-OKYC-VERIFY-REQ@1', response: 'CF-OKYC-VERIFY-RES@1' },
			send_otp: { request: 'CF-OKYC-OTP-REQ@2', response: 'CF-OKYC-OTP-RES@1' }
		});
		expect(specs[0]!.audit.payloads).toBe('sealed');
	});

	it('shows the validation report at the field its pointer names', async () => {
		aadhaarDraft();
		server.use(
			mockOperation('validateIntegrationVersion', {
				valid: false,
				checks: [
					{
						check: 'operations_have_transformers',
						status: 'fail',
						message:
							'integrations/int_aadhaar_okyc/versions/1.yaml: CF-OKYC-OTP-REQ@1 is compiled for operation send_otp, not verify_otp',
						pointer: '/targets/0/operations/verify_otp'
					},
					{
						check: 'references_exist',
						status: 'fail',
						message: 'unknown account',
						pointer: '/targets/0/account_id'
					},
					{
						check: 'supports_features',
						status: 'fail',
						message: 'no failover',
						pointer: '/failover/0'
					}
				]
			})
		);
		const { user, target } = await renderEditor();
		await user.click(screen.getByRole('button', { name: 'Run publish checks on v1' }));
		const verify = within(target).getByRole('listitem', { name: 'verify_otp' });
		expect(
			await within(verify).findByText(
				'CF-OKYC-OTP-REQ@1 is compiled for operation send_otp, not verify_otp'
			)
		).toBeInTheDocument();
		expect(within(target).getByLabelText('Account')).toHaveAttribute('aria-invalid', 'true');
		expect(within(target).getByText('unknown account')).toBeInTheDocument();
		// No field shows /failover: listed above the form with its path.
		expect(
			screen.getByText(/Problems outside these fields/).closest('[role="alert"]')
		).toHaveTextContent('failover[0]: no failover');
		expect(screen.getByRole('button', { name: 'Publish v1' })).toBeDisabled();
	});

	it('checks the weights live and refuses to save while the form has problems', async () => {
		aadhaarDraft();
		const specs = captureSave();
		const { user } = await renderEditor();
		await user.click(screen.getByRole('button', { name: 'Add target' }));
		const t2 = screen.getByRole('listitem', { name: /^Target 2/ });
		await user.type(within(t2).getByLabelText('Weight (%)'), '20');
		const total = within(screen.getByRole('group', { name: 'Targets' })).getAllByRole('status')[0]!;
		expect(total).toHaveTextContent('Weights total 120: they must total 100.');
		await user.type(
			screen.getByLabelText(/^Why you are changing the draft/),
			'Second target for the canary'
		);
		await user.click(screen.getByRole('button', { name: 'Save draft v1' }));
		expect(await within(t2).findByText('Pick the vendor.')).toBeInTheDocument();
		expect(within(t2).getByLabelText('Vendor')).toHaveAttribute('aria-invalid', 'true');
		expect(screen.getByText(/Fix the \d+ problems marked above/)).toBeInTheDocument();
		expect(specs).toHaveLength(0);
	});

	it('offers an operation the spec lists but the capability lacks for removal', async () => {
		const spec = structuredClone(AADHAAR);
		spec.targets[0]!.operations!.bogus = { request: 'A@1', response: 'B@1' };
		aadhaarDraft(spec);
		const { user, target } = await renderEditor();
		expect(target).toHaveTextContent('Not an operation of this capability');
		await user.click(within(target).getByRole('button', { name: 'Remove bogus' }));
		expect(target).not.toHaveTextContent('bogus');
	});

	it('switches to YAML with the same spec, and back only when the YAML parses', async () => {
		aadhaarDraft();
		const { user } = await renderEditor();
		await user.click(screen.getByRole('button', { name: 'YAML' }));
		const yaml = screen.getByLabelText(/^Spec/) as HTMLTextAreaElement;
		const parsed = parseSpecYaml(yaml.value);
		expect(parsed.ok && parsed.value).toEqual(AADHAAR);
		await user.type(yaml, '\n  bad: [[');
		await user.click(screen.getByRole('button', { name: 'Form' }));
		expect(screen.getByRole('alert')).toHaveTextContent(/Fix the YAML first/);
		expect(screen.getByRole('button', { name: 'YAML' })).toHaveAttribute('aria-pressed', 'true');
	});

	it('a 403 on the capability only empties the lookups; typed values still work', async () => {
		aadhaarDraft();
		server.use(mockError('getCapability', 403), mockError('listTransformers', 403));
		const data = (await load({
			fetch,
			url: new URL(`http://console.test/integrations/${ID}/publish`),
			params: { integration_id: ID },
			parent: async () => ({ access: editor })
		} as never)) as object;
		render(Page, { props: { data: { access: editor, ...data } as never } });
		const target = await screen.findByRole('listitem', { name: /^Target 1/ });
		// The spec's own operations still show (verify_otp is listed in it).
		expect(within(target).getByLabelText('Request transformer for verify_otp')).toHaveValue(
			'CF-OKYC-VERIFY-REQ@1'
		);
	});
});

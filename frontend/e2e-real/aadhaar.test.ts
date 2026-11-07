import { expect, test, type Locator, type Page } from '@playwright/test';
import { aadhaarOperations, createAadhaarObjects, type AadhaarObjects } from './aadhaar-setup';
import { changeIdOf, RUN, shot, signIn } from './helpers';

// HUB-292 against the real stack: the Aadhaar e-KYC example `int_aadhaar_okyc` (examples/
// aadhaar-okyc) built in the form editor. An interactive integration with two operations,
// `send_otp` and `verify_otp`, each with its request schema (ADR-0023, HUB-441); one target,
// `send_otp` on the default transformer pair and `verify_otp` on its own pair
// (`TargetRef.operations`), sync delivery, sealed audit. A pair that reads fields verify_otp does
// not declare (send_otp's request transformer reads req.aadhaar_number) is refused by the Hub's
// publish checks and shown on that operation's fields.

const INT = `int_aadhaar_okyc_${RUN}`;
let o: AadhaarObjects;

/** Renames an operation of the draft (applied on Enter) and returns its list item. */
async function renameOperation(page: Page, from: string, to: string): Promise<Locator> {
	const name = page.getByRole('listitem', { name: `Operation ${from}` }).getByLabel('Name', {
		exact: true
	});
	await name.fill(to);
	await name.press('Enter');
	const op = page.getByRole('listitem', { name: `Operation ${to}` });
	await expect(op).toBeVisible();
	return op;
}

/** Sets an operation's request body schema in the JSON Schema view of its editor. */
async function setBodySchema(op: Locator, schema: Record<string, unknown>) {
	const body = op.getByRole('group', { name: 'Request body (required)' });
	await body.getByRole('button', { name: 'Edit as JSON Schema' }).click();
	await body.getByLabel('Request body as JSON Schema').fill(JSON.stringify(schema, null, 2));
	await body.getByRole('button', { name: 'Fields', exact: true }).click();
	await expect(body.getByRole('checkbox', { name: /required$/ }).first()).toBeChecked();
}

test('int_aadhaar_okyc with per-operation transformers, validated and published', async ({
	page
}) => {
	await signIn(page, { role: 'admin', next: '/integrations/new' });
	o = await createAadhaarObjects(page, RUN);

	await page.getByLabel(/^Integration id/).fill(INT);
	await page.getByLabel(/^Owner tenant/).selectOption('orders-service');
	await page.getByLabel('Description').fill('Aadhaar OTP e-KYC (example)');
	// Interactive is fixed at creation: the two operations are steps of one session.
	await page.getByRole('checkbox', { name: /^Interactive/ }).check();
	await page.getByRole('checkbox', { name: /Moves money or uses up a quota/ }).check();
	await page.getByLabel(/^Reason/).fill('Aadhaar e-KYC for loan onboarding');
	await page.getByRole('button', { name: 'Create integration' }).click();
	await expect(page).toHaveURL(`/integrations/${INT}/publish`);

	// The operations and their request schemas, as in the example's version file.
	const schemas = aadhaarOperations();
	const send = await renameOperation(page, 'default', 'send_otp');
	await send
		.getByLabel('Description (shown to callers)')
		.fill('Send an OTP to the mobile number linked to the Aadhaar number.');
	await setBodySchema(send, schemas.send_otp!);
	await page.getByRole('button', { name: 'Add operation' }).click();
	const verify = await renameOperation(page, 'operation_2', 'verify_otp');
	await verify
		.getByLabel('Description (shown to callers)')
		.fill('Verify the OTP inside the session send_otp opened.');
	await setBodySchema(verify, schemas.verify_otp!);

	await page.getByRole('button', { name: 'Add target' }).click();
	const target = page.getByRole('listitem', { name: /^Target 1/ });
	await target.getByLabel('Vendor').fill(o.vendor);
	await target.getByLabel('Account').fill(o.account);
	await target.getByLabel('Weight (%)').fill('100');
	await target.getByLabel('Request transformer', { exact: true }).fill(`${o.otpReq}@1`);
	await target.getByLabel('Response transformer (optional)', { exact: true }).fill(`${o.otpRes}@1`);
	// Both operations of the integration are listed; send_otp stays on the default pair.
	const sendOtp = target.getByRole('listitem', { name: 'send_otp' });
	const verifyOtp = target.getByRole('listitem', { name: 'verify_otp' });
	await expect(sendOtp).toContainText('Uses the default pair.');
	// First the wrong pair for verify_otp (send_otp's, reading req.aadhaar_number, which
	// verify_otp's request schema does not declare): the Hub's check fails there.
	await verifyOtp.getByRole('checkbox', { name: 'Own transformers for verify_otp' }).check();
	await expect(verifyOtp.getByLabel('Request transformer for verify_otp')).toHaveValue(
		`${o.otpReq}@1`
	);
	await page.getByLabel('Mode').selectOption('sync');
	await page.getByLabel('Payloads in the audit trail').selectOption('sealed');
	await page.getByLabel(/^Why this version/).fill('Aadhaar OTP e-KYC, first version');
	await page.getByRole('button', { name: 'Create draft' }).click();
	await changeIdOf(page, 'Saved draft v1');
	await expect(verifyOtp.locator('.fk-error').first()).toBeVisible();
	await expect(verifyOtp).toContainText(/verify_otp/);
	await expect(page.getByRole('button', { name: 'Publish v1' })).toBeDisabled();
	await shot(page, 'integration-aadhaar-error');

	await verifyOtp.getByLabel('Request transformer for verify_otp').fill(`${o.verifyReq}@1`);
	await verifyOtp.getByLabel('Response transformer for verify_otp').fill(`${o.verifyRes}@1`);
	await page.getByLabel(/^Why you are changing the draft/).fill('verify_otp uses its own pair');
	await page.getByRole('button', { name: 'Save draft v1' }).click();
	await changeIdOf(page, 'All publish checks pass');
	await expect(verifyOtp.locator('.fk-error')).toHaveCount(0);

	// The same draft as YAML: the operations' schemas, and operations.verify_otp next to the
	// default transformer pair.
	await page.getByRole('button', { name: 'YAML' }).click();
	const yaml = page.getByLabel(/^Spec/);
	await expect(yaml).toHaveValue(
		new RegExp(`operations:\\s+verify_otp:\\s+request: ${o.verifyReq}@1`)
	);
	await expect(yaml).toHaveValue(/aadhaar_number:/);
	await expect(yaml).toHaveValue(/payloads: sealed/);
	await page.getByRole('button', { name: 'Form' }).click();
	await shot(page, 'integration-aadhaar');

	await page.getByRole('button', { name: 'Publish v1' }).click();
	const dialog = page.getByRole('dialog', { name: `Publish ${INT} v1` });
	await dialog.getByLabel(/^Reason/).fill('Aadhaar e-KYC live for loan onboarding');
	await dialog.getByRole('button', { name: 'Publish v1' }).click();
	await changeIdOf(page, 'Published v1');

	await page.goto(`/integrations/${INT}`);
	await expect(page.getByText(`${o.verifyReq}@1`).first()).toBeVisible();
});

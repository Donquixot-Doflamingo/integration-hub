import { expect, test } from '@playwright/test';
import { CAPABILITY, createAadhaarObjects, type AadhaarObjects } from './aadhaar-setup';
import { changeIdOf, RUN, shot, signIn } from './helpers';

// HUB-292 against the real stack: the Aadhaar e-KYC example `int_aadhaar_okyc` (examples/
// aadhaar-okyc) built in the form editor. One target, `send_otp` on the default transformer pair
// and `verify_otp` on its own pair (`TargetRef.operations`), sync delivery, sealed audit. A pair
// compiled for the other operation is caught by the Hub's `operations_have_transformers` check and
// shown on that operation's fields.

const INT = `int_aadhaar_okyc_${RUN}`;
let o: AadhaarObjects;

test('int_aadhaar_okyc with per-operation transformers, validated and published', async ({
	page
}) => {
	await signIn(page, { role: 'admin', next: '/integrations/new' });
	o = await createAadhaarObjects(page, RUN);

	await page.getByLabel(/^Integration id/).fill(INT);
	await page.getByLabel(/^Owner tenant/).selectOption('orders-service');
	await page.getByLabel(/^Capability/).selectOption(CAPABILITY);
	await page.getByLabel('Description').fill('Aadhaar OTP e-KYC (example)');
	await page.getByRole('checkbox', { name: /Moves money or uses up a quota/ }).check();
	await page.getByLabel(/^Reason/).fill('Aadhaar e-KYC for loan onboarding');
	await page.getByRole('button', { name: 'Create integration' }).click();
	await expect(page).toHaveURL(`/integrations/${INT}/publish`);

	await page.getByRole('button', { name: 'Add target' }).click();
	const target = page.getByRole('listitem', { name: /^Target 1/ });
	await target.getByLabel('Vendor').fill(o.vendor);
	await target.getByLabel('Account').fill(o.account);
	await target.getByLabel('Weight (%)').fill('100');
	await target.getByLabel('Request transformer', { exact: true }).fill(`${o.otpReq}@1`);
	await target.getByLabel('Response transformer', { exact: true }).fill(`${o.otpRes}@1`);
	// Both operations of the capability are listed; send_otp stays on the default pair.
	const sendOtp = target.getByRole('listitem', { name: 'send_otp' });
	const verifyOtp = target.getByRole('listitem', { name: 'verify_otp' });
	await expect(sendOtp).toContainText('Uses the default pair.');
	// First the wrong pair for verify_otp (compiled for send_otp): the Hub's check fails there.
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

	// The same draft as YAML: operations.verify_otp next to the default transformer pair.
	await page.getByRole('button', { name: 'YAML' }).click();
	const yaml = page.getByLabel(/^Spec/);
	await expect(yaml).toHaveValue(
		new RegExp(`operations:\\s+verify_otp:\\s+request: ${o.verifyReq}@1`)
	);
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

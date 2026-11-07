import { expect, test, type Page } from '@playwright/test';
import { lingering, NOWHERE } from '../e2e/helpers';
import { changeIdOf, expectNoSeriousA11yViolations, shot, signIn, uid } from './helpers';

// HUB-291 against the real stack: create an account with write-only credentials, test the
// connection, rotate (add next -> test -> promote -> delete old), edit config and quota with
// If-Match, drain and retire, and what each role sees.

const ACCOUNT = uid('e2e-acct');
// Fake values for mockvendor; distinctive so the test can look for them anywhere in the page.
const SECRET_1 = `e2e-passcode-one-${Date.now()}`;
const SECRET_2 = `e2e-passcode-two-${Date.now()}`;

test.describe.configure({ mode: 'serial' });

async function noSecretIn(page: Page, secret: string) {
	expect(await lingering(page, secret)).toEqual(NOWHERE);
}

test('admin creates an account; the secret is cleared on submit and never shown', async ({
	page
}) => {
	const posted: string[] = [];
	page.on('request', (r) => {
		if (r.method() === 'POST') posted.push(r.url());
	});
	await signIn(page, { role: 'admin', next: '/accounts' });
	await shot(page, 'accounts-list');
	await page.getByRole('link', { name: 'Create account' }).click();
	await expect(page).toHaveURL('/accounts/new');

	// First with an id the dev seed already uses: the API refuses (409) and the secret is gone.
	await page.getByLabel(/^Account id/).fill('ct-brandA-in');
	await page.getByLabel(/^Vendor/).selectOption('clevertap');
	await page.getByLabel(/^Region/).selectOption('in');
	await page.getByLabel(/^Config/).fill('{"project_id": "E2E-PROJECT", "brand_code": "E"}');
	await page.getByLabel('Quota (requests/s)').fill('20');
	await page.getByLabel('Key').fill('passcode');
	await expect(page.getByLabel('Secret value')).toHaveAttribute('type', 'password');
	await page.getByLabel('Secret value').fill(SECRET_1);
	await page.getByRole('button', { name: 'Add credential' }).click();
	await page.getByLabel('Key').nth(1).fill('account_id');
	await page.getByLabel('Secret value').nth(1).fill('E2E-ACCOUNT');
	await page.getByLabel(/^Reason/).fill('E2E account for brand E');
	await shot(page, 'account-new');
	await page.getByRole('button', { name: 'Create account' }).click();
	await expect(page.getByRole('alert').filter({ hasText: 'CONFLICT' })).toContainText(
		'ct-brandA-in'
	);
	await expect(page.getByText('The secret values were cleared when you submitted')).toBeVisible();
	await expect(page.getByLabel('Secret value').first()).toHaveValue('');
	await expect(page.getByLabel('Key').first()).toHaveValue('passcode');
	await noSecretIn(page, SECRET_1);

	await page.getByLabel(/^Account id/).fill(ACCOUNT);
	await page.getByLabel('Secret value').first().fill(SECRET_1);
	await page.getByLabel('Secret value').nth(1).fill('E2E-ACCOUNT');
	await page.getByRole('button', { name: 'Create account' }).click();
	await expect(page).toHaveURL(`/accounts/${ACCOUNT}`);
	await changeIdOf(page, `Created account ${ACCOUNT}`);
	await expect(page.getByRole('region', { name: 'Credential slots' })).toContainText('Active');
	await expect(page.getByRole('region', { name: 'Credential slots' })).toContainText('v1');
	await noSecretIn(page, SECRET_1);
	expect(posted.some((u) => u.includes(SECRET_1))).toBe(false);
	await expectNoSeriousA11yViolations(page);
});

test('admin tests the connection and rotates: add next, test, promote, delete old', async ({
	page
}) => {
	await signIn(page, { role: 'admin', next: `/accounts/${ACCOUNT}` });
	const test1 = page.getByRole('form', { name: `Test connection of ${ACCOUNT}` });
	await test1.getByRole('button', { name: 'Test connection' }).click();
	await expect(page.getByRole('status').filter({ hasText: 'Connection works' })).toContainText(
		'Active credential v1'
	);

	await page.getByRole('button', { name: 'Add next credential' }).click();
	const add = page.getByRole('dialog', { name: `Add the next credential to ${ACCOUNT}` });
	await add.getByLabel('Key').fill('passcode');
	await add.getByLabel('Secret value').fill(SECRET_2);
	await add.getByLabel(/^Reason/).fill('Quarterly passcode rotation');
	await add.getByRole('button', { name: 'Store next credential' }).click();
	await expect(add).toBeHidden();
	await changeIdOf(page, 'Stored the next credential');
	await noSecretIn(page, SECRET_2);
	const slots = page.getByRole('region', { name: 'Credential slots' });
	await expect(slots).toContainText('Next (not used yet)');
	await expect(page.getByRole('list', { name: 'Rotation steps' })).toContainText('Now Test it');
	await shot(page, 'account-detail');

	await test1.getByLabel('Credential').selectOption('next');
	await test1.getByRole('button', { name: 'Test connection' }).click();
	await expect(page.getByRole('status').filter({ hasText: 'Connection works' })).toContainText(
		'Next credential v2'
	);

	await page.getByRole('button', { name: 'Promote v2' }).click();
	const promote = page.getByRole('dialog', { name: `Promote credential v2 of ${ACCOUNT}` });
	await expect(promote.getByRole('note')).toHaveCount(0); // it passed a test on this page
	await promote.getByLabel(/^Reason/).fill('Connection test passed for v2');
	await promote.getByRole('button', { name: 'Promote v2' }).click();
	await changeIdOf(page, 'Promoted the next credential');
	await expect(slots).toContainText('Previous (kept, not used)');

	await page.getByRole('button', { name: 'Delete previous credential v1' }).click();
	const del = page.getByRole('dialog', { name: `Delete credential v1 of ${ACCOUNT}` });
	await del.getByLabel(/^Reason/).fill('No auth failures for 15 minutes');
	await expect(del.getByRole('button', { name: 'Delete v1' })).toBeDisabled();
	await del.getByLabel(/^Type/).fill(ACCOUNT);
	await del.getByRole('button', { name: 'Delete v1' }).click();
	await changeIdOf(page, 'Deleted the previous credential');
	await expect(slots).not.toContainText('Previous');
	await expect(slots).toContainText('v2');
	await noSecretIn(page, SECRET_2);
});

test('admin edits config and quota with If-Match, then drains and retires', async ({ page }) => {
	await signIn(page, { role: 'admin', next: `/accounts/${ACCOUNT}` });
	await page.getByRole('button', { name: 'Edit config and quota' }).click();
	const edit = page.getByRole('dialog', { name: `Edit config and quota of ${ACCOUNT}` });
	await edit.getByLabel(/^Config/).fill('{"project_id": "E2E-PROJECT-2"}');
	await edit.getByLabel('Quota (requests/s)').fill('40');
	await edit.getByLabel(/^Reason/).fill('Vendor raised the quota, VND-1');
	const post = page.waitForRequest((r) => r.method() === 'POST' && r.url().includes('?/update'));
	await edit.getByRole('button', { name: 'Save changes' }).click();
	expect(new URLSearchParams((await post).postData() ?? '').get('if_match')).toMatch(/^".+"$/);
	await changeIdOf(page, `Saved ${ACCOUNT}`);
	await expect(page.getByText('E2E-PROJECT-2')).toBeVisible();
	await expect(page.getByText('brand_code')).toHaveCount(0); // removed by the merge-patch
	await expect(page.getByRole('definition').filter({ hasText: '40 requests/s' })).toBeVisible();

	await page.getByRole('button', { name: 'Start draining' }).click();
	let dialog = page.getByRole('dialog', { name: `Start draining: ${ACCOUNT}` });
	await dialog.getByLabel(/^Reason/).fill('Brand E moves to a new workspace');
	await dialog.getByRole('button', { name: 'Start draining' }).click();
	await changeIdOf(page, 'state Draining');

	await page.getByRole('button', { name: 'Retire account' }).click();
	dialog = page.getByRole('dialog', { name: `Retire account: ${ACCOUNT}` });
	await dialog.getByLabel(/^Reason/).fill('Workspace closed at the vendor');
	await dialog.getByLabel(/^Type/).fill(ACCOUNT);
	await dialog.getByRole('button', { name: 'Retire account' }).click();
	await changeIdOf(page, 'state Retired');
	await expect(page.getByText('No credentials: retiring deleted them.')).toBeVisible();
	await expect(page.getByRole('button', { name: /Retire|draining|Edit config/ })).toHaveCount(0);
});

test('a lifecycle change the Hub refuses shows its reason, not a reload prompt', async ({
	page
}) => {
	// ct-brandB-us is targeted by active versions in the dev seed; the Hub refuses to drain it
	// (see the report: the contract lists active -> draining as allowed).
	await signIn(page, { role: 'admin', next: '/accounts/ct-brandB-us' });
	await page.getByRole('button', { name: 'Start draining' }).click();
	const dialog = page.getByRole('dialog', { name: 'Start draining: ct-brandB-us' });
	await dialog.getByLabel(/^Reason/).fill('E2E: check the in-use guard');
	await dialog.getByRole('button', { name: 'Start draining' }).click();
	const alert = dialog.getByRole('alert');
	await expect(alert).toContainText('CONFLICT');
	await expect(alert).toContainText('ct-brandB-us');
	await expect(alert).toContainText(/req_[0-9A-Z]{26}/);
	await expect(alert).not.toContainText('Someone else changed');
	await expect(dialog.getByLabel(/^Reason/)).toHaveValue('E2E: check the in-use guard');
	await dialog.getByRole('button', { name: 'Cancel' }).click();
	await expect(page.getByText('Active', { exact: true }).first()).toBeVisible();
});

test('editors test connections only; viewers read; tenant viewers see nothing', async ({
	page
}) => {
	await signIn(page, { role: 'editor', next: '/accounts/ct-brandA-in' });
	await expect(page.getByRole('form', { name: 'Test connection of ct-brandA-in' })).toBeVisible();
	for (const name of [
		/Add next credential/,
		/Promote/,
		/Delete previous/,
		/Edit config/,
		/draining/,
		/Retire/
	])
		await expect(page.getByRole('button', { name })).toHaveCount(0);
	await expect(page.getByText('Rotating credentials needs the Admin role.')).toBeVisible();
	await page.goto('/accounts');
	await expect(page.getByRole('link', { name: 'Create account' })).toHaveCount(0);
	await page.context().clearCookies();

	await signIn(page, { role: 'viewer', next: '/accounts/ct-brandA-in' });
	await expect(page.getByRole('region', { name: 'Credential slots' })).toBeVisible();
	await expect(page.getByRole('form', { name: /Test connection/ })).toHaveCount(0);
	await expect(page.getByText('Testing the connection needs the Editor role.')).toBeVisible();
	await page.context().clearCookies();

	await signIn(page, { role: 'tenant_viewer', tenants: 'orders-service', next: '/accounts' });
	await expect(page.getByRole('note')).toContainText('tenant viewers cannot see them');
});

import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn, useScenario } from './helpers';

// HUB-260: weights (total must be 100) and pause / resume with a mandatory reason, on mocks.

const ID = 'int_evt_brandA';

test.use({ viewport: { width: 1280, height: 900 } });

test('weights: blocked until they total 100, then a new version is published', async ({ page }) => {
	await useScenario(page, 'v8-active');
	await signIn(page, { next: `/integrations/${ID}` });
	await page.getByRole('button', { name: 'Set weights' }).click();
	const dialog = page.getByRole('dialog', { name: `Set weights of ${ID}` });
	const a = dialog.getByLabel('clevertap/ct-brandA-in', { exact: true });
	const b = dialog.getByLabel('clevertap/ct-brandA-in-v2');
	await expect(a).toHaveValue('90');
	await dialog.getByLabel(/Reason/).fill('Canary healthy for 24 h, go 50/50');
	await a.fill('60');
	await b.fill('50');
	await expect(dialog.getByText('Total 110%: must be 100')).toBeVisible();
	const confirm = dialog.getByRole('button', { name: 'Publish v9' });
	await expect(confirm).toBeDisabled();
	await a.fill('50');
	await expect(dialog.getByText('Total 100%')).toBeVisible();
	const post = page.waitForRequest((r) => r.method() === 'POST' && r.url().includes('?/weights'));
	await confirm.click();
	const sent = new URLSearchParams((await post).postData() ?? '');
	expect([sent.get('weight_0'), sent.get('weight_1'), sent.get('publish')]).toEqual([
		'50',
		'50',
		'on'
	]);
	await expect(page.getByRole('status').filter({ hasText: 'Weights set' })).toContainText(
		'v9 is now active'
	);
	await expect(page.getByText('chg_01J9Z4B2C3D4E5F6G7H8J9K0N9')).toBeVisible();
});

test('pause by keyboard: impact shown, reason required, change id shown', async ({ page }) => {
	await signIn(page, { next: `/integrations/${ID}` });
	const pause = page.getByRole('button', { name: 'Pause integration' });
	await pause.focus();
	await page.keyboard.press('Enter');
	const dialog = page.getByRole('dialog', { name: `Pause ${ID}` });
	await expect(dialog).toContainText('503 INTEGRATION_PAUSED');
	await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
	await page.keyboard.press('Tab');
	await expect(dialog.getByLabel(/Reason/)).toBeFocused();
	await page.keyboard.type('Vendor reports data corruption');
	await page.keyboard.press('Tab');
	await page.keyboard.press('Tab');
	await expect(dialog.getByRole('button', { name: 'Pause', exact: true })).toBeFocused();
	await page.keyboard.press('Enter');
	await expect(page.getByRole('status').filter({ hasText: `Paused ${ID}` })).toBeVisible();
	await expect(page.getByText('chg_01J9Z4B2C3D4E5F6G7H8J9K0N7')).toBeVisible();
});

test('resume a paused integration', async ({ page }) => {
	await useScenario(page, 'paused');
	await signIn(page, { next: `/integrations/${ID}` });
	await page.getByRole('button', { name: 'Resume integration' }).click();
	const dialog = page.getByRole('dialog', { name: `Resume ${ID}` });
	await dialog.getByLabel(/Reason/).fill('Vendor confirmed fix');
	await dialog.getByRole('button', { name: 'Resume' }).click();
	await expect(page.getByRole('status').filter({ hasText: `Resumed ${ID}` })).toBeVisible();
});

test('a 403 on pause keeps the dialog and shows the request id', async ({ page }) => {
	await useScenario(page, 'pauseIntegration:403');
	await signIn(page, { next: `/integrations/${ID}` });
	await page.getByRole('button', { name: 'Pause integration' }).click();
	const dialog = page.getByRole('dialog');
	await dialog.getByLabel(/Reason/).fill('Vendor reports data corruption');
	await dialog.getByRole('button', { name: 'Pause', exact: true }).click();
	await expect(dialog.getByRole('alert')).toContainText('FORBIDDEN');
	await expect(dialog.getByRole('alert')).toContainText('req_01J9Z3M1A5B7C9D1E3F5G7H9KE');
});

for (const scheme of ['light', 'dark'] as const) {
	test(`weights and pause dialogs are axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await useScenario(page, 'v8-active');
		await signIn(page, { next: `/integrations/${ID}` });
		await page.getByRole('button', { name: 'Set weights' }).click();
		await page.getByRole('dialog').getByLabel('clevertap/ct-brandA-in', { exact: true }).fill('70');
		await expect(page.getByRole('dialog').getByText(/must be 100/)).toBeVisible();
		await expectNoSeriousA11yViolations(page);
		await page.keyboard.press('Escape');
		await page.getByRole('button', { name: 'Pause integration' }).click();
		await expect(page.getByRole('dialog')).toBeVisible();
		await expectNoSeriousA11yViolations(page);
	});
}

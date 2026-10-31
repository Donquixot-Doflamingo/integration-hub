import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn, useScenario } from './helpers';

// HUB-262: breaker force controls on the health board (admin only), on mocks.

test.use({ viewport: { width: 1280, height: 900 } });

test('force open: affected integrations, typed account id, reason, change id', async ({ page }) => {
	await signIn(page, { role: 'admin', next: '/health' });
	await page.getByRole('button', { name: 'Force open' }).click();
	const dialog = page.getByRole('dialog', { name: 'Force ct-brandA-in upload@1 open' });
	await expect(dialog).toContainText('Affects 1 integration: int_evt_brandA');
	const confirm = dialog.getByRole('button', { name: 'Force open' });
	await dialog.getByLabel(/Reason/).fill('Vendor maintenance window 10:00-10:30 IST');
	await expect(confirm).toBeDisabled();
	await dialog.getByLabel(/Type ct-brandA-in to confirm/).fill('ct-brandA-in');
	await dialog.getByLabel('Hold for').selectOption({ label: '1 hour' });
	const post = page.waitForRequest((r) => r.method() === 'POST' && r.url().includes('?/forceOpen'));
	await confirm.click();
	const sent = new URLSearchParams((await post).postData() ?? '');
	expect([sent.get('confirm'), sent.get('duration_ms'), sent.get('reason')]).toEqual([
		'ct-brandA-in',
		'3600000',
		'Vendor maintenance window 10:00-10:30 IST'
	]);
	await expect(page.getByRole('status').filter({ hasText: 'Forced open until' })).toContainText(
		'chg_01J9Z4B2C3D4E5F6G7H8J9K0NQ'
	);
});

test('clear a forced breaker', async ({ page }) => {
	await useScenario(page, 'breaker-forced');
	await signIn(page, { role: 'admin', next: '/health' });
	await page.getByRole('button', { name: 'Clear force' }).click();
	const dialog = page.getByRole('dialog');
	await dialog.getByLabel(/Reason/).fill('Maintenance finished early');
	await dialog.getByRole('button', { name: 'Clear force' }).click();
	await expect(page.getByRole('status').filter({ hasText: 'Force cleared' })).toBeVisible();
});

test('force dialog by keyboard; Escape returns focus', async ({ page }) => {
	await signIn(page, { role: 'admin', next: '/health' });
	const trigger = page.getByRole('button', { name: 'Force closed' });
	await trigger.focus();
	await page.keyboard.press('Enter');
	const dialog = page.getByRole('dialog');
	await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
	await page.keyboard.press('Escape');
	await expect(dialog).toBeHidden();
	await expect(trigger).toBeFocused();
});

test('editors and viewers get no force controls', async ({ page }) => {
	await signIn(page, { role: 'editor', next: '/health' });
	await expect(page.getByRole('table')).toBeVisible();
	await expect(page.getByRole('columnheader', { name: 'Force' })).toHaveCount(0);
	await expect(page.getByRole('button', { name: /^Force/ })).toHaveCount(0);
});

for (const scheme of ['light', 'dark'] as const) {
	test(`force dialog is axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await signIn(page, { role: 'admin', next: '/health' });
		await expectNoSeriousA11yViolations(page);
		await page.getByRole('button', { name: 'Force open' }).click();
		await expect(page.getByRole('dialog')).toContainText('Affects 1 integration');
		await expectNoSeriousA11yViolations(page);
	});
}

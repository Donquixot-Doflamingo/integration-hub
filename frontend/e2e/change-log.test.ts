import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-274: change log filters in the URL, entry with before / after as a diff. On MSW.

test.use({ viewport: { width: 1280, height: 900 } });

test('filter in the URL, open an entry, see the diff', async ({ page }) => {
	await signIn(page, { role: 'viewer', next: '/change-log' });
	await page.getByLabel('Actor', { exact: true }).fill('priya@example.com');
	await page.getByLabel('Object type').selectOption({ label: 'Integration' });
	await page.getByRole('button', { name: 'Apply' }).click();
	await expect(page).toHaveURL('/change-log?actor=priya%40example.com&object_type=integration');
	await expect(page.getByRole('link', { name: /Remove filter Actor/ })).toBeVisible();
	await page.reload();
	await expect(page.getByLabel('Actor', { exact: true })).toHaveValue('priya@example.com');
	await page.getByRole('link', { name: 'chg_01J9Z4B2C3D4E5F6G7H8J9K0N5' }).click();
	await expect(page).toHaveURL('/change-log/chg_01J9Z4B2C3D4E5F6G7H8J9K0N5');
	await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link')).toHaveText([
		'Console',
		'Change log',
		'chg_01J9Z4B2C3D4E5F6G7H8J9K0N5'
	]);
	const row = page.getByRole('table', { name: /Changed fields/ }).getByRole('row', {
		name: /active_version/
	});
	await expect(row).toContainText('Changed');
	await expect(row).toContainText('7');
	await expect(row).toContainText('8');
	await page.getByText('Full before and after').click();
	await expect(page.getByText('"active_version": 8')).toBeVisible();
});

test('a change notice links its change log entry', async ({ page }) => {
	await signIn(page, { role: 'admin', next: '/tenants/orders-service' });
	await page.getByRole('button', { name: 'Disable tenant' }).click();
	const dialog = page.getByRole('dialog', { name: 'Disable tenant orders-service' });
	await dialog.getByLabel(/Reason/).fill('Offboarding the orders team, OPS-300');
	await dialog.getByLabel(/to confirm/).fill('orders-service');
	await dialog.getByRole('button', { name: 'Disable' }).click();
	await page.getByRole('link', { name: 'Open the change log entry' }).click();
	await expect(page).toHaveURL('/change-log/chg_01J9Z4B2C3D4E5F6G7H8J9K0NB');
	await expect(page.getByRole('heading', { level: 1 })).toHaveText(
		'chg_01J9Z4B2C3D4E5F6G7H8J9K0NB'
	);
});

for (const scheme of ['light', 'dark'] as const) {
	test(`change log pages are axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await signIn(page, { role: 'viewer', next: '/change-log?object_type=integration' });
		await expect(page.getByRole('table')).toBeVisible();
		await expectNoSeriousA11yViolations(page);
		await page.goto('/change-log/chg_01J9Z4B2C3D4E5F6G7H8J9K0N5');
		await page.getByText('Full before and after').click();
		await expectNoSeriousA11yViolations(page);
	});
}

import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-261: health board on MSW (contract example: one open breaker on clevertap/ct-brandA-in).

test.use({ viewport: { width: 1280, height: 900 } });

test('open breakers come first and are marked in text', async ({ page }) => {
	await signIn(page, { role: 'viewer', next: '/health' });
	const table = page.getByRole('table');
	await expect(table.getByRole('rowheader').first()).toContainText('clevertap');
	const row = table.getByRole('row').nth(2);
	await expect(row.getByRole('rowheader')).toContainText('ct-brandA-in');
	await expect(row).toContainText('Open');
	await expect(row).toContainText('82%');
	await expect(row).toContainText('1,290');
	await expect(row).toContainText('Active');
	await expect(page.getByRole('status').filter({ hasText: 'open,' })).toContainText(
		'1 open, 0 half-open, 0 closed'
	);
});

test('auto-refresh: interval, pause by keyboard, refresh now', async ({ page }) => {
	await page.clock.install();
	await signIn(page, { role: 'viewer', next: '/health' });
	await expect(page.getByRole('table')).toBeVisible();
	await expect(page.getByText(/Updated just now/)).toBeVisible();
	await page.clock.fastForward(3000);
	await expect(page.getByText(/Updated 3 s ago/)).toBeVisible();
	// The 5 s tick reloads the board.
	await page.clock.fastForward(2500);
	await expect(page.getByText(/Updated just now/)).toBeVisible();

	const pause = page.getByRole('button', { name: 'Pause auto-refresh' });
	await pause.focus();
	await page.keyboard.press('Enter');
	await expect(page.getByRole('button', { name: 'Resume auto-refresh' })).toBeFocused();
	await page.clock.fastForward(12_000);
	await expect(page.getByText(/Paused\. Updated 12 s ago/)).toBeVisible();
	await page.getByRole('button', { name: 'Refresh now' }).click();
	await expect(page.getByText(/Paused\. Updated just now/)).toBeVisible();

	await page.getByRole('button', { name: 'Resume auto-refresh' }).click();
	await page.getByLabel('Refresh every').selectOption('30');
	await page.clock.fastForward(10_000);
	await expect(page.getByText(/Updated 10 s ago/)).toBeVisible();
});

for (const scheme of ['light', 'dark'] as const) {
	test(`health board is axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await signIn(page, { role: 'viewer', next: '/health' });
		await expect(page.getByRole('table')).toBeVisible();
		await expectNoSeriousA11yViolations(page);
	});
}

test('tenant viewers get a note instead of the board', async ({ page }) => {
	await signIn(page, { role: 'tenant_viewer', tenants: 'orders-service', next: '/health' });
	await expect(page.getByRole('note')).toContainText('tenant viewers cannot see');
});

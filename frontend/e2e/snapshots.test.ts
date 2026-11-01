import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-273: snapshots with the latest highlighted and an expandable diff summary. On MSW.

test.use({ viewport: { width: 1280, height: 900 } });

test('latest is marked; the diff summary expands by keyboard and links the object', async ({
	page
}) => {
	await signIn(page, { role: 'viewer', next: '/snapshots' });
	const latest = page.getByRole('region', { name: 'Latest snapshot' });
	await expect(latest).toContainText('Snapshot 413');
	const table = page.getByRole('table', { name: /Registry snapshots/ });
	await expect(table.getByRole('rowheader').first()).toContainText('Latest');
	// The page never scrolls sideways; wide tables scroll inside their own region.
	expect(
		await page.evaluate(
			() => document.documentElement.scrollWidth - document.documentElement.clientWidth
		)
	).toBe(0);
	const toggle = table.getByRole('button', { name: 'Show changes in 413' });
	await toggle.focus();
	await page.keyboard.press('Enter');
	await expect(table.getByRole('button', { name: 'Hide changes in 413' })).toHaveAttribute(
		'aria-expanded',
		'true'
	);
	await expect(table).toContainText('Versus snapshot 412:');
	await expect(table).toContainText('Updated integration int_evt_brandA');
	await table.getByRole('link', { name: 'int_evt_brandA' }).click();
	await expect(page).toHaveURL('/integrations/int_evt_brandA');
});

for (const scheme of ['light', 'dark'] as const) {
	test(`snapshots are axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await signIn(page, { role: 'viewer', next: '/snapshots' });
		await page.getByRole('button', { name: 'Show changes in 413' }).click();
		await expect(page.getByText('Updated integration')).toBeVisible();
		await expectNoSeriousA11yViolations(page);
	});
}

import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-263: request explorer search on MSW; filters live in the URL.

test.use({ viewport: { width: 1280, height: 900 } });

test('search by filters: URL is shareable and survives reload', async ({ page }) => {
	await signIn(page, { role: 'viewer', next: '/requests' });
	await page.getByLabel('Integration').fill('int_evt_brandA');
	await page.getByRole('checkbox', { name: 'Dead-lettered' }).check();
	await page.getByRole('checkbox', { name: 'Parked' }).check();
	await page.getByLabel(/^Error codes/).fill('upstream_timeout');
	await page.getByLabel('From (UTC)').fill('2026-10-31T00:00');
	await page.getByLabel('To (UTC)').fill('2026-11-01T00:00');
	await page.getByLabel(/^Error codes/).press('Enter');
	await expect(page).toHaveURL(
		'/requests?integration_id=int_evt_brandA&status=parked%2Cdead_lettered&code=UPSTREAM_TIMEOUT&from=2026-10-31T00%3A00%3A00Z&to=2026-11-01T00%3A00%3A00Z'
	);
	await expect(page.getByRole('table')).toContainText('req_01J9Z3K8X2V7Q4M6N8P0R2S4T6');
	await page.reload();
	await expect(page.getByRole('checkbox', { name: 'Parked' })).toBeChecked();
	await expect(page.getByLabel('From (UTC)')).toHaveValue('2026-10-31T00:00');
	const chips = page.getByRole('list', { name: 'Active filters' });
	await expect(chips.getByRole('link')).toHaveCount(5);
	await chips.getByRole('link', { name: /Remove filter Error code/ }).click();
	await expect(page).not.toHaveURL(/code=/);
	await page.getByRole('link', { name: 'Clear all' }).click();
	await expect(page).toHaveURL('/requests');
});

test('a range over 31 days is refused before calling the API', async ({ page }) => {
	await signIn(page, {
		role: 'viewer',
		next: '/requests?from=2026-09-01T00:00:00Z&to=2026-11-01T00:00:00Z'
	});
	await expect(page.getByRole('alert')).toContainText('at most 31 days');
	await expect(page.getByRole('table')).toHaveCount(0);
});

test('paging keeps the filters', async ({ page }) => {
	await signIn(page, { role: 'viewer', next: '/requests?status=succeeded' });
	await page.getByRole('link', { name: /Next/ }).click();
	await expect(page).toHaveURL(/status=succeeded&cursor=/);
	await expect(page.getByRole('link', { name: /Previous/ })).toBeVisible();
});

for (const scheme of ['light', 'dark'] as const) {
	test(`request explorer is axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await signIn(page, {
			role: 'viewer',
			next: '/requests?status=parked&tenant_id=orders-service'
		});
		await expect(page.getByRole('table')).toBeVisible();
		await expectNoSeriousA11yViolations(page);
		await page.goto('/requests?from=2026-11-01T00:00:00Z&to=2026-10-01T00:00:00Z');
		await expect(page.getByRole('alert')).toBeVisible();
		await expectNoSeriousA11yViolations(page);
	});
}

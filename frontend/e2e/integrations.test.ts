import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-256: integrations list on MSW (contract example: one integration).

test.describe('integrations list', () => {
	test.use({ viewport: { width: 1280, height: 800 } });

	test('filters live in the URL, survive reload and are removable', async ({ page }) => {
		await signIn(page, { next: '/integrations' });
		await expect(page.getByRole('rowheader').first()).toContainText('int_evt_brandA');

		const listCall = page.waitForRequest(
			(r) => r.url().includes('/admin/v1/integrations?') && r.url().includes('paused=false')
		);
		await page.getByLabel('Search', { exact: true }).fill('brand');
		await page.getByLabel('Owner tenant', { exact: true }).fill('orders-service');
		await page.getByLabel('State', { exact: true }).selectOption('false');
		await page.getByRole('button', { name: 'Apply' }).click();
		await expect(page).toHaveURL('/integrations?q=brand&tenant_id=orders-service&paused=false');
		const sent = new URL((await listCall).url()).searchParams;
		expect(Object.fromEntries(sent)).toMatchObject({
			q: 'brand',
			tenant_id: 'orders-service',
			paused: 'false'
		});

		await page.reload();
		await expect(page.getByLabel('Search', { exact: true })).toHaveValue('brand');
		const chips = page.getByRole('list', { name: 'Active filters' });
		await expect(chips.getByRole('link')).toHaveCount(3);
		await expectNoSeriousA11yViolations(page);

		await chips.getByRole('link', { name: 'Remove filter Search: brand' }).click();
		await expect(page).toHaveURL('/integrations?tenant_id=orders-service&paused=false');
		await page.getByRole('link', { name: 'Clear all' }).click();
		await expect(page).toHaveURL('/integrations');
		await expect(page.getByRole('list', { name: 'Active filters' })).toHaveCount(0);
	});

	test('filters work by keyboard: Enter in a field applies them', async ({ page }) => {
		await signIn(page, { next: '/integrations' });
		await page.getByLabel('Vendor', { exact: true }).focus();
		await page.keyboard.type('clevertap');
		await page.keyboard.press('Enter');
		await expect(page).toHaveURL('/integrations?vendor_id=clevertap');
		await expect(page.getByLabel('Vendor', { exact: true })).toBeFocused();
		await expect(page.getByRole('navigation', { name: 'Pagination' })).toContainText('Page 1');
	});

	for (const scheme of ['light', 'dark'] as const) {
		test(`axe-clean in ${scheme}, with live and state badges in text`, async ({ page }) => {
			await page.emulateMedia({ colorScheme: scheme });
			await signIn(page, { next: '/integrations?paused=false' });
			await expect(page.getByRole('row').nth(1)).toContainText('Live');
			await expectNoSeriousA11yViolations(page);
		});
	}

	test('a tenant viewer filters among their tenants only', async ({ page }) => {
		await signIn(page, {
			role: 'tenant_viewer',
			tenants: 'orders-service, billing',
			next: '/integrations'
		});
		const tenant = page.getByLabel('Owner tenant', { exact: true });
		await expect(tenant.locator('option')).toHaveText([
			'All my tenants',
			'orders-service',
			'billing'
		]);
		await expect(page.getByLabel('Vendor', { exact: true })).toHaveCount(0);
		await tenant.selectOption('billing');
		await page.getByRole('button', { name: 'Apply' }).click();
		await expect(page).toHaveURL('/integrations?tenant_id=billing');
	});
});

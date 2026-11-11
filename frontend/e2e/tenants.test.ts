import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn, useScenario } from './helpers';

// HUB-270: tenants list, detail, create and edit (admin), tenant viewers' scope. On MSW.

test.use({ viewport: { width: 1280, height: 900 } });

const REASON = 'Pilot tenant for the orders team';

test('admin creates a tenant: checked form, reason, change id', async ({ page }) => {
	await signIn(page, { role: 'admin', next: '/tenants' });
	await page.getByRole('button', { name: 'Create tenant' }).click();
	const dialog = page.getByRole('dialog', { name: 'Create a tenant' });
	await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
	await dialog.getByLabel(/Tenant id/).fill('a b');
	await dialog.getByLabel(/Display name/).fill('Orders service');
	await expect(dialog.getByLabel(/Tenant id/)).toHaveAttribute('aria-invalid', 'true');
	await dialog.getByLabel(/Tenant id/).fill('orders-service');
	await dialog.getByLabel('Requests per second').fill('200');
	await dialog.getByLabel(/Reason/).fill(REASON);
	const post = page.waitForRequest((r) => r.method() === 'POST' && r.url().includes('?/create'));
	await dialog.getByRole('button', { name: 'Create tenant' }).click();
	expect(new URLSearchParams((await post).postData() ?? '').get('tenant_id')).toBe(
		'orders-service'
	);
	const notice = page.getByRole('status').filter({ hasText: 'Created tenant orders-service' });
	await expect(notice).toContainText('chg_01J9Z4B2C3D4E5F6G7H8J9K0NA');
	await expect(page.getByRole('button', { name: 'Create tenant' })).toBeFocused();
	await notice.getByRole('link', { name: 'Open orders-service' }).click();
	await expect(page).toHaveURL('/tenants/orders-service');
	await expect(page.getByRole('heading', { level: 1 })).toHaveText('orders-service');
});

test('edit sends If-Match; a concurrent change offers Reload', async ({ page }) => {
	await useScenario(page, 'updateTenant:412');
	await signIn(page, { role: 'admin', next: '/tenants/orders-service' });
	await page.getByRole('button', { name: 'Edit tenant' }).click();
	const dialog = page.getByRole('dialog', { name: 'Edit tenant orders-service' });
	await dialog.getByLabel('Burst').fill('600');
	await dialog.getByLabel(/Reason/).fill('Festive season traffic, OPS-142');
	const post = page.waitForRequest((r) => r.method() === 'POST' && r.url().includes('?/update'));
	await dialog.getByRole('button', { name: 'Save changes' }).click();
	const form = new URLSearchParams((await post).postData() ?? '');
	expect(form.get('if_match')).toBe('"3"');
	await expect(dialog.getByRole('alert')).toContainText('Someone else changed orders-service');
	await expect(dialog.getByRole('button', { name: 'Reload' })).toBeVisible();
	await dialog.getByRole('button', { name: 'Cancel' }).click();
	await expect(dialog).toBeHidden();
	await expect(page.getByRole('button', { name: 'Edit tenant' })).toBeFocused();
});

test('a tenant viewer sees only their tenants and cannot open another', async ({ page }) => {
	await signIn(page, { role: 'tenant_viewer', tenants: 'orders-service', next: '/tenants' });
	await expect(page.getByRole('rowheader')).toHaveText(['orders-service']);
	await expect(page.getByRole('button', { name: 'Create tenant' })).toHaveCount(0);
	await page.goto('/tenants/billing');
	await expect(page.getByRole('heading', { level: 1 })).toHaveText('Not found');
	await expect(page.getByText('Tenant billing is not one of your tenants.')).toBeVisible();
});

for (const scheme of ['light', 'dark'] as const) {
	test(`tenant pages are axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await signIn(page, { role: 'admin', next: '/tenants' });
		await expect(page.getByRole('table')).toBeVisible();
		await expectNoSeriousA11yViolations(page);
		await page.getByRole('button', { name: 'Create tenant' }).click();
		await expectNoSeriousA11yViolations(page);
		await page.goto('/tenants/orders-service');
		await expect(page.getByRole('region', { name: 'Summary' })).toBeVisible();
		await expectNoSeriousA11yViolations(page);
		await page.getByRole('button', { name: 'Edit tenant' }).click();
		await expectNoSeriousA11yViolations(page);
	});
}

// HUB-485: tenants from ISFC (the contract examples carry an `isfc` block).
for (const scheme of ['light', 'dark'] as const) {
	test(`ISFC tenant list and detail are axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await signIn(page, { role: 'admin', next: '/tenants' });
		await expect(page.getByText('Active in ISFC').first()).toBeVisible();
		await expect(page.getByRole('button', { name: 'Sync now' })).toBeVisible();
		await expectNoSeriousA11yViolations(page);
		await page.goto('/tenants/orders-service');
		await expect(page.getByRole('region', { name: 'ISFC' })).toContainText('ORDERS');
		await expectNoSeriousA11yViolations(page);
	});
}

test('a tenant admin completes the setup of a pending tenant', async ({ page }) => {
	await useScenario(page, 'tenant-pending');
	await signIn(page, {
		role: 'tenant_admin',
		tenants: 'orders-service',
		next: '/tenants/orders-service'
	});
	const panel = page.getByRole('region', { name: /Finish setting up this tenant/ });
	await expect(panel).toBeVisible();
	await expectNoSeriousA11yViolations(page);
	await panel.getByRole('button', { name: 'Complete setup' }).click();
	const dialog = page.getByRole('dialog', { name: 'Complete setup of orders-service' });
	await dialog.getByLabel(/Reason/).fill('Checked with the orders team, OPS-9');
	const post = page.waitForRequest(
		(r) => r.method() === 'POST' && r.url().includes('?/completeSetup')
	);
	await dialog.getByRole('button', { name: 'Complete setup' }).click();
	await post;
	await expect(
		page.getByRole('status').filter({ hasText: 'Setup of orders-service is complete' })
	).toBeVisible();
});

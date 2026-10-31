import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-253: dev sign-in (mock build), redirect to login, session cookie, sign out.

test('unauthenticated users are sent to login and come back to the page they asked for', async ({
	page
}) => {
	await page.goto('/dlq?tenant_id=orders-service');
	await expect(page).toHaveURL('/login?next=%2Fdlq%3Ftenant_id%3Dorders-service');
	await expect(page.getByRole('heading', { level: 1 })).toHaveText('Sign in');
	await expectNoSeriousA11yViolations(page);

	await page.getByRole('radio', { name: /^Viewer\b/ }).check();
	await page.getByLabel('Name').fill('Vik Viewer');
	await page.getByRole('button', { name: 'Sign in' }).click();
	await expect(page).toHaveURL('/dlq?tenant_id=orders-service');
	const user = page.getByRole('button', { name: /Vik Viewer/ });
	await expect(user).toContainText('Viewer');

	// The session lives in an httpOnly cookie; nothing auth-related in web storage.
	const cookies = await page.context().cookies();
	const session = cookies.find((c) => c.name === 'hub_console_session');
	expect(session).toMatchObject({ httpOnly: true, sameSite: 'Lax', path: '/' });
	expect(await page.evaluate(() => document.cookie)).not.toContain('hub_console_session');
	const storage = await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }));
	expect(storage).not.toMatch(/session|token/i);
});

test('a tenant viewer must name a tenant; the error is tied to the field', async ({ page }) => {
	await page.goto('/login');
	await page.getByRole('radio', { name: /^Tenant viewer\b/ }).check();
	const tenants = page.getByLabel(/^Tenants/);
	await tenants.fill('');
	await page.getByRole('button', { name: 'Sign in' }).click();
	await expect(tenants).toHaveAttribute('aria-invalid', 'true');
	await expect(page.getByText('A tenant viewer needs at least one tenant')).toBeVisible();
	await expect(page.getByRole('radio', { name: /^Tenant viewer\b/ })).toBeChecked();

	await tenants.fill('orders-service');
	await page.getByRole('button', { name: 'Sign in' }).click();
	await expect(page.getByRole('heading', { level: 1 })).toHaveText('Integration Hub console');
	await page.getByRole('button', { name: /Priya Sharma/ }).click();
	await expect(page.locator('#user-panel')).toContainText('orders-service');
	await expect(page.locator('#user-panel')).toContainText('Tenant viewer');
});

test('sign in works by keyboard alone', async ({ page }) => {
	await page.goto('/login');
	await page.getByRole('radio', { name: /^Editor\b/ }).focus();
	await page.keyboard.press('ArrowUp');
	await expect(page.getByRole('radio', { name: /^Admin\b/ })).toBeChecked();
	await page.getByRole('button', { name: 'Sign in' }).focus();
	await page.keyboard.press('Enter');
	await expect(page.getByRole('button', { name: /Priya Sharma/ })).toContainText('Admin');
});

test('sign out ends the session', async ({ page }) => {
	await signIn(page, { role: 'admin' });
	await page.getByRole('button', { name: /Priya Sharma/ }).click();
	await page.getByRole('button', { name: 'Sign out' }).click();
	await expect(page).toHaveURL('/login');
	await page.goto('/integrations');
	await expect(page).toHaveURL('/login?next=%2Fintegrations');
});

for (const scheme of ['light', 'dark'] as const) {
	test(`login page is axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await page.goto('/login');
		await expect(page.locator('html')).toHaveAttribute('data-theme', scheme);
		await expectNoSeriousA11yViolations(page);
	});
}

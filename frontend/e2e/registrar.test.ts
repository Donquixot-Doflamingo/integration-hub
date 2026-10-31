import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn, useScenario } from './helpers';

// HUB-272: callback (registrar) endpoints on the tenant page. On MSW.

test.use({ viewport: { width: 1280, height: 900 } });

const PAGE = '/tenants/orders-service';

test('register: the SSRF refusal lands on the URL field', async ({ page }) => {
	await useScenario(page, 'registrar-url-private');
	await signIn(page, { role: 'admin', next: PAGE });
	await page.getByRole('button', { name: 'Register endpoint' }).click();
	const dialog = page.getByRole('dialog', {
		name: 'Register a callback endpoint for orders-service'
	});
	await dialog.getByLabel(/Endpoint id/).fill('reg_orders_cb');
	await dialog.getByLabel(/Callback URL/).fill('https://10.0.0.12/cb');
	await dialog.getByLabel(/Reason/).fill('Callbacks for async integrations');
	await dialog.getByRole('button', { name: 'Register' }).click();
	const url = dialog.getByLabel(/Callback URL/);
	await expect(url).toHaveAttribute('aria-invalid', 'true');
	await expect(url).toHaveAccessibleDescription(/private or link-local address/);
	await expectNoSeriousA11yViolations(page);
});

test('register and rotate never show a signing secret', async ({ page }) => {
	const bodies: string[] = [];
	page.on('response', async (r) => {
		if (r.request().method() === 'POST') bodies.push(await r.text().catch(() => ''));
	});
	await signIn(page, { role: 'admin', next: PAGE });
	await page.getByRole('button', { name: 'Register endpoint' }).click();
	const dialog = page.getByRole('dialog', { name: /Register a callback endpoint/ });
	await dialog.getByLabel(/Endpoint id/).fill('reg_orders_cb');
	await dialog.getByLabel(/Callback URL/).fill('https://orders.internal/hub/callbacks');
	await dialog.getByLabel(/Reason/).fill('Callbacks for async integrations');
	await dialog.getByRole('button', { name: 'Register' }).click();
	await expect(
		page.getByRole('status').filter({ hasText: 'Registered callback endpoint' })
	).toContainText('The console never shows signing secrets.');

	await page.getByRole('button', { name: 'Rotate secret of reg_orders_cb' }).click();
	const rotate = page.getByRole('dialog', { name: /Rotate the signing secret/ });
	await rotate.getByLabel(/Reason/).fill('Scheduled rotation');
	await rotate.getByRole('button', { name: 'Rotate secret' }).click();
	await expect(
		page.getByRole('status').filter({ hasText: 'Rotated the signing secret' })
	).toContainText('callbacks carry both signatures');
	expect(await page.content()).not.toContain('whsec_');
	// The form action answers reached the browser without the secret.
	expect(bodies.filter((b) => b.includes('whsec_'))).toEqual([]);
	expect(bodies.length).toBeGreaterThanOrEqual(2);
});

for (const scheme of ['light', 'dark'] as const) {
	test(`endpoint dialogs are axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await signIn(page, { role: 'admin', next: PAGE });
		await expect(page.getByRole('table', { name: /Callback endpoints of/ })).toBeVisible();
		await page.getByRole('button', { name: 'Edit reg_orders_cb' }).click();
		await expectNoSeriousA11yViolations(page);
		await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();
		await page.getByRole('button', { name: 'Delete reg_orders_cb' }).click();
		await expectNoSeriousA11yViolations(page);
	});
}

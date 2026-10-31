import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, lingering, NOWHERE, signIn, useScenario } from './helpers';

// HUB-272: callback (registrar) endpoints on the tenant page; signing secrets shown once. On MSW:
// the secrets are the contract examples (createRegistrarEndpoint / rotateRegistrarSecret).

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

const CREATED = 'whsec_EXAMPLEexampleEXAMPLEexample00';
const ROTATED = 'whsec_NEWexampleEXAMPLEexampleEXAM00';

test('register: the signing secret is shown once with copy and a warning, then never again', async ({
	page,
	context
}) => {
	await context.grantPermissions(['clipboard-read', 'clipboard-write']);
	const consoleLines: string[] = [];
	page.on('console', (m) => consoleLines.push(m.text()));
	const urls: string[] = [];
	page.on('request', (r) => urls.push(r.url()));

	await signIn(page, { role: 'admin', next: PAGE });
	await page.getByRole('button', { name: 'Register endpoint' }).click();
	const dialog = page.getByRole('dialog', { name: /Register a callback endpoint/ });
	await dialog.getByLabel(/Endpoint id/).fill('reg_orders_cb');
	await dialog.getByLabel(/Callback URL/).fill('https://orders.internal/hub/callbacks');
	await dialog.getByLabel(/Reason/).fill('Callbacks for async integrations');
	await dialog.getByRole('button', { name: 'Register' }).click();

	const reveal = page.getByRole('dialog', {
		name: 'Copy the callback signing secret of reg_orders_cb'
	});
	const field = reveal.getByLabel('Signing secret version 1 of reg_orders_cb');
	await expect(field).toHaveValue(CREATED);
	await expect(field).toBeFocused();
	await expect(reveal).toContainText('This is the only time it is shown.');
	await expect(reveal).toContainText('verify the X-Hub-Signature header of every callback');
	await expectNoSeriousA11yViolations(page);
	await reveal.getByRole('button', { name: 'Copy' }).click();
	expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(CREATED);
	await page.keyboard.press('Escape');
	await expect(reveal).toBeVisible();
	await reveal.getByLabel('I have stored it somewhere safe').check();
	await reveal.getByRole('button', { name: 'Done' }).click();
	await expect(reveal).toBeHidden();
	await expect(page.getByRole('button', { name: 'Register endpoint' })).toBeFocused();
	await expect(
		page.getByRole('status').filter({ hasText: 'Registered callback endpoint' })
	).toContainText('chg_01J9Z4B2C3D4E5F6G7H8J9K0NF');

	expect(await lingering(page, CREATED)).toEqual(NOWHERE);
	await page.reload();
	await expect(page.getByRole('table', { name: /Callback endpoints of/ })).toBeVisible();
	expect(await lingering(page, CREATED)).toEqual(NOWHERE);
	await page.goBack();
	await page.goForward();
	await expect(page.getByRole('table', { name: /Callback endpoints of/ })).toBeVisible();
	expect(await lingering(page, CREATED)).toEqual(NOWHERE);
	expect(consoleLines.filter((l) => l.includes('whsec_'))).toEqual([]);
	expect(urls.filter((u) => u.includes('whsec_'))).toEqual([]);
});

test('rotate: the new signing secret is shown once with the overlap', async ({ page }) => {
	await signIn(page, { role: 'admin', next: PAGE });
	await page.getByRole('button', { name: 'Rotate secret of reg_orders_cb' }).click();
	const rotate = page.getByRole('dialog', { name: /Rotate the signing secret/ });
	await rotate.getByLabel(/Reason/).fill('Scheduled rotation');
	await rotate.getByRole('button', { name: 'Rotate secret' }).click();
	const reveal = page.getByRole('dialog', {
		name: 'Copy the new callback signing secret of reg_orders_cb'
	});
	await expect(reveal.getByLabel('Signing secret version 2 of reg_orders_cb')).toHaveValue(ROTATED);
	await expect(reveal).toContainText(
		'Until 2026-11-01 10:00 UTC, callbacks carry both signatures (version 2 and 1)'
	);
	await reveal.getByLabel('I have stored it somewhere safe').check();
	await reveal.getByRole('button', { name: 'Done' }).click();
	expect(await lingering(page, ROTATED)).toEqual(NOWHERE);
	await page.reload();
	await expect(page.getByRole('table', { name: /Callback endpoints of/ })).toBeVisible();
	expect(await lingering(page, ROTATED)).toEqual(NOWHERE);
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
		await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();
		await page.getByRole('button', { name: 'Rotate secret of reg_orders_cb' }).click();
		await page
			.getByRole('dialog')
			.getByLabel(/Reason/)
			.fill('Scheduled rotation');
		await page.getByRole('button', { name: 'Rotate secret', exact: true }).click();
		await expect(
			page.getByRole('dialog', { name: /Copy the new callback signing secret/ })
		).toBeVisible();
		await expectNoSeriousA11yViolations(page);
	});
}

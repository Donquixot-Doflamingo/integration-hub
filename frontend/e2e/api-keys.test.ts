import { expect, test, type Page } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-271: API keys on the tenant page; the full key is shown exactly once. On MSW: the secrets
// are the contract examples (createApiKey / rotateApiKey).

test.use({ viewport: { width: 1280, height: 900 } });

const CREATED = 'hk_live_7Qm2EXAMPLEexampleEXAMPLE0000';
const ROTATED = 'hk_live_Z9a1EXAMPLEexampleEXAMPLE0000';
const PAGE = '/tenants/orders-service';

/** Every place in the browser the secret could still be after the reveal closed. */
async function lingering(page: Page, secret: string) {
	return page.evaluate((s) => {
		const values = [...document.querySelectorAll('input, textarea')].map(
			(e) => (e as HTMLInputElement).value
		);
		const storage = [localStorage, sessionStorage].flatMap((st) =>
			Object.keys(st).map((k) => `${k}=${st.getItem(k)}`)
		);
		return {
			html: document.documentElement.outerHTML.includes(s),
			inputs: values.some((v) => v.includes(s)),
			storage: storage.some((v) => v.includes(s)),
			url: location.href.includes(s),
			history: JSON.stringify(history.state ?? null).includes(s)
		};
	}, secret);
}
const NOWHERE = { html: false, inputs: false, storage: false, url: false, history: false };

test('create: the key is shown once with copy and a warning, then never again', async ({
	page,
	context
}) => {
	await context.grantPermissions(['clipboard-read', 'clipboard-write']);
	const consoleLines: string[] = [];
	page.on('console', (m) => consoleLines.push(m.text()));
	const urls: string[] = [];
	page.on('request', (r) => urls.push(r.url()));

	await signIn(page, { role: 'admin', next: PAGE });
	await page.getByRole('button', { name: 'Create API key' }).click();
	const dialog = page.getByRole('dialog', { name: 'Create an API key for orders-service' });
	await dialog.getByLabel('Description').fill('orders prod');
	await dialog.getByLabel(/Reason/).fill('Initial production key, OPS-201');
	await dialog.getByRole('button', { name: 'Create key' }).click();

	const reveal = page.getByRole('dialog', { name: 'Copy the new API key for orders-service' });
	const field = reveal.getByLabel(/New API key hk_live_7Qm2/);
	await expect(field).toHaveValue(CREATED);
	await expect(field).toBeFocused();
	await expect(reveal).toContainText('This is the only time it is shown.');
	await expectNoSeriousA11yViolations(page);
	await reveal.getByRole('button', { name: 'Copy' }).click();
	await expect(reveal.getByText('Copied to the clipboard.')).toBeVisible();
	expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(CREATED);
	await page.keyboard.press('Escape');
	await expect(reveal).toBeVisible();
	await expect(reveal.getByRole('button', { name: 'Done' })).toBeDisabled();
	await reveal.getByLabel('I have stored it somewhere safe').check();
	await reveal.getByRole('button', { name: 'Done' }).click();
	await expect(reveal).toBeHidden();
	await expect(page.getByRole('button', { name: 'Create API key' })).toBeFocused();
	await expect(page.getByRole('status').filter({ hasText: 'Created API key' })).toContainText(
		'chg_01J9Z4B2C3D4E5F6G7H8J9K0NC'
	);

	expect(await lingering(page, CREATED)).toEqual(NOWHERE);
	await page.reload();
	await expect(page.getByRole('table', { name: /API keys of/ })).toBeVisible();
	expect(await lingering(page, CREATED)).toEqual(NOWHERE);
	await page.goBack();
	await page.goForward();
	await expect(page.getByRole('table', { name: /API keys of/ })).toBeVisible();
	expect(await lingering(page, CREATED)).toEqual(NOWHERE);
	expect(consoleLines.filter((l) => l.includes(CREATED))).toEqual([]);
	expect(urls.filter((u) => u.includes(CREATED))).toEqual([]);
});

test('rotate: the replacement is shown once with the old key overlap expiry', async ({ page }) => {
	await signIn(page, { role: 'admin', next: PAGE });
	await page.getByRole('button', { name: 'Rotate hk_live_7Qm2' }).click();
	const dialog = page.getByRole('dialog', { name: 'Rotate API key hk_live_7Qm2' });
	await expect(dialog.getByLabel('Overlap')).toHaveValue('86400000');
	await dialog.getByLabel(/Reason/).fill('Scheduled rotation, OPS-202');
	const post = page.waitForRequest((r) => r.method() === 'POST' && r.url().includes('?/rotateKey'));
	await dialog.getByRole('button', { name: 'Rotate' }).click();
	expect(new URLSearchParams((await post).postData() ?? '').get('overlap_ms')).toBe('86400000');
	const reveal = page.getByRole('dialog', { name: /Copy the replacement API key/ });
	await expect(reveal.getByLabel(/hk_live_Z9a1/)).toHaveValue(ROTATED);
	await expect(reveal).toContainText(
		'The old key hk_live_7Qm2 keeps working until 2026-11-01 10:00 UTC (overlap 24 h)'
	);
	await reveal.getByLabel('I have stored it somewhere safe').check();
	await reveal.getByRole('button', { name: 'Done' }).click();
	expect(await lingering(page, ROTATED)).toEqual(NOWHERE);
});

test('revoke needs the prefix typed; viewers see no key controls', async ({ page }) => {
	await signIn(page, { role: 'admin', next: PAGE });
	await page.getByRole('button', { name: 'Revoke hk_live_7Qm2' }).click();
	const dialog = page.getByRole('dialog', { name: 'Revoke API key hk_live_7Qm2' });
	await dialog.getByLabel(/Reason/).fill('Key leaked in a CI log');
	await expect(dialog.getByRole('button', { name: 'Revoke' })).toBeDisabled();
	await dialog.getByLabel(/to confirm/).fill('hk_live_7Qm2');
	await dialog.getByRole('button', { name: 'Revoke' }).click();
	await expect(page.getByRole('status').filter({ hasText: 'Revoked API key' })).toContainText(
		'chg_01J9Z4B2C3D4E5F6G7H8J9K0ND'
	);

	await signIn(page, { role: 'viewer', next: PAGE });
	await expect(page.getByRole('table', { name: /API keys of/ })).toContainText('hk_live_7Qm2');
	await expect(page.getByRole('button', { name: /Create API key|Rotate|Revoke/ })).toHaveCount(0);
});

for (const scheme of ['light', 'dark'] as const) {
	test(`key dialogs are axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await signIn(page, { role: 'admin', next: PAGE });
		await expect(page.getByRole('table', { name: /API keys of/ })).toBeVisible();
		await expectNoSeriousA11yViolations(page);
		await page.getByRole('button', { name: 'Create API key' }).click();
		await expectNoSeriousA11yViolations(page);
		await page
			.getByRole('dialog')
			.getByLabel(/Reason/)
			.fill('Initial production key, OPS-201');
		await page.getByRole('button', { name: 'Create key' }).click();
		await expect(page.getByRole('dialog', { name: /Copy the new API key/ })).toBeVisible();
		await expectNoSeriousA11yViolations(page);
	});
}

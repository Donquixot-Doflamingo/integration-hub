import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn, useScenario } from './helpers';

// HUB-259: publish and rollback with a mandatory reason, on MSW (browser) + mocked form actions
// (server). The contract examples: v8 draft over active v7; scenario `v8-active` for rollback.

const ID = 'int_evt_brandA';
const REASON = 'Canary 20% to the new workspace';

test.use({ viewport: { width: 1280, height: 900 } });

test('publish: diff first, reason required, change id shown', async ({ page }) => {
	await signIn(page, { next: `/integrations/${ID}` });
	await page.getByRole('link', { name: 'Review draft v8 and publish' }).click();
	await expect(page).toHaveURL(`/integrations/${ID}/publish`);
	await expect(
		page.getByRole('region', { name: 'Changes against the active version' })
	).toContainText('v7 to v8');
	// The draft opens in the form editor (HUB-292); the YAML tab shows the same spec.
	await expect(page.getByRole('listitem', { name: /^Target 2/ }).getByLabel('Account')).toHaveValue(
		'ct-brandA-in-v2'
	);
	await page.getByRole('button', { name: 'YAML' }).click();
	await expect(page.getByLabel(/^Spec/)).toHaveValue(/ct-brandA-in-v2/);

	const trigger = page.getByRole('button', { name: 'Publish v8' });
	await trigger.click();
	const dialog = page.getByRole('dialog', { name: `Publish ${ID} v8` });
	await expect(dialog).toContainText('replacing v7');
	const confirm = dialog.getByRole('button', { name: 'Publish v8' });
	await expect(confirm).toBeDisabled();
	await dialog.getByLabel(/Reason/).fill(REASON);
	const post = page.waitForRequest((r) => r.method() === 'POST' && r.url().includes('?/publish'));
	await confirm.click();
	expect(new URLSearchParams((await post).postData() ?? '').get('reason')).toBe(REASON);
	await expect(dialog).toBeHidden();
	const notice = page.getByRole('status').filter({ hasText: 'Published v8' });
	await expect(notice).toContainText('It is now the active version (was v7)');
	await expect(notice).toContainText('chg_01J9Z4B2C3D4E5F6G7H8J9K0N5');
	await expect(trigger).toBeFocused();
});

test('publish: a 409 from the server asks for a reload and keeps the reason', async ({ page }) => {
	await useScenario(page, 'publishIntegrationVersion:409');
	await signIn(page, { next: `/integrations/${ID}/publish` });
	await page.getByRole('button', { name: 'Publish v8' }).click();
	const dialog = page.getByRole('dialog');
	await dialog.getByLabel(/Reason/).fill(REASON);
	await dialog.getByRole('button', { name: 'Publish v8' }).click();
	await expect(dialog.getByRole('alert')).toContainText(`Someone else changed ${ID}`);
	await expect(dialog.getByLabel(/Reason/)).toHaveValue(REASON);
	await expectNoSeriousA11yViolations(page);
});

test('rollback: creates a new version from an older one', async ({ page }) => {
	await useScenario(page, 'v8-active');
	await signIn(page, { next: `/integrations/${ID}` });
	await page.getByRole('button', { name: 'Roll back to v7' }).click();
	const dialog = page.getByRole('dialog', { name: `Roll back ${ID} to v7` });
	await expect(dialog).toContainText('Creates v9 with the content of v7');
	await dialog.getByLabel(/Reason/).fill('Canary account returning UPSTREAM_REJECTED');
	const post = page.waitForRequest((r) => r.method() === 'POST' && r.url().includes('?/rollback'));
	await dialog.getByRole('button', { name: 'Roll back' }).click();
	const sent = new URLSearchParams((await post).postData() ?? '');
	expect(sent.get('reason')).toBe('Canary account returning UPSTREAM_REJECTED');
	expect(sent.get('to_version')).toBe('7');
	expect(sent.get('expected_active_version')).toBe('8');
	await expect(page.getByRole('status').filter({ hasText: 'Rolled back' })).toContainText(
		'v9 is now active, a copy of v7'
	);
	await expect(page.getByText('chg_01J9Z4B2C3D4E5F6G7H8J9K0N6')).toBeVisible();
});

test('rollback dialog by keyboard: Cancel first, Tab stays inside, Escape returns focus', async ({
	page
}) => {
	await useScenario(page, 'v8-active');
	await signIn(page, { next: `/integrations/${ID}` });
	const trigger = page.getByRole('button', { name: 'Roll back to v7' });
	await trigger.focus();
	await page.keyboard.press('Enter');
	const dialog = page.getByRole('dialog');
	await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
	for (let i = 0; i < 6; i++) {
		await page.keyboard.press('Tab');
		expect(await dialog.evaluate((d) => d.contains(document.activeElement))).toBe(true);
	}
	await page.keyboard.press('Escape');
	await expect(dialog).toBeHidden();
	await expect(trigger).toBeFocused();
});

for (const scheme of ['light', 'dark'] as const) {
	test(`publish page and dialogs are axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await useScenario(page, 'v8-active');
		await signIn(page, { next: `/integrations/${ID}` });
		await page.getByRole('button', { name: 'Roll back to v7' }).click();
		await expect(page.getByRole('dialog')).toBeVisible();
		await page
			.getByRole('dialog')
			.getByLabel(/Reason/)
			.fill('short');
		await page
			.getByRole('dialog')
			.getByLabel(/Reason/)
			.blur();
		await expectNoSeriousA11yViolations(page);
		await page.keyboard.press('Escape');
		await page.context().clearCookies({ name: 'hub_mock_scenario' });
		await page.goto(`/integrations/${ID}/publish`);
		await expect(
			page.getByRole('region', { name: 'Changes against the active version' })
		).toBeVisible();
		await expectNoSeriousA11yViolations(page);
		await page.getByRole('button', { name: 'Publish v8' }).click();
		await expect(page.getByRole('dialog')).toBeVisible();
		await expectNoSeriousA11yViolations(page);
	});
}

test('viewers get neither publish nor rollback', async ({ page }) => {
	await useScenario(page, 'v8-active');
	await signIn(page, { role: 'viewer', next: `/integrations/${ID}` });
	await expect(page.getByRole('table', { name: /Versions of/ })).toBeVisible();
	await expect(page.getByRole('button', { name: /Roll back/ })).toHaveCount(0);
	await expect(page.getByRole('link', { name: /publish|new draft/i })).toHaveCount(0);
	await page.goto(`/integrations/${ID}/publish`);
	await expect(page.getByText(/Read only\. Editing and publishing versions/)).toBeVisible();
});

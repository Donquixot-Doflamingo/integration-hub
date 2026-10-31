import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-257: integration detail and version history on MSW (contract examples: v8 draft, v7 active).

test.describe('integration detail', () => {
	test.use({ viewport: { width: 1280, height: 900 } });

	test('opens from the list and shows the active spec and the history', async ({ page }) => {
		await signIn(page, { next: '/integrations' });
		await page.getByRole('link', { name: 'int_evt_brandA' }).click();
		await expect(page).toHaveURL('/integrations/int_evt_brandA');
		await expect(page.getByRole('heading', { level: 1 })).toHaveText('int_evt_brandA');
		await expect(page.getByRole('heading', { name: 'Active version v7' })).toBeVisible();
		await expect(page.getByRole('region', { name: 'Targets' })).toContainText(
			'clevertap/ct-brandA-in'
		);

		const history = page.getByRole('table', { name: /Versions of int_evt_brandA/ });
		await expect(history.getByRole('rowheader')).toHaveText(['v8', 'v7']);
		await expect(history.getByRole('row').nth(1)).toContainText('Draft');
		await expect(history.getByRole('row').nth(2)).toContainText('Active');
		await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link')).toHaveText(
			['Console', 'Integrations', 'int_evt_brandA']
		);
	});

	for (const scheme of ['light', 'dark'] as const) {
		test(`detail and version pages are axe-clean in ${scheme}`, async ({ page }) => {
			await page.emulateMedia({ colorScheme: scheme });
			await signIn(page, { next: '/integrations/int_evt_brandA' });
			await expect(page.getByRole('heading', { name: 'Active version v7' })).toBeVisible();
			await expect(page.getByRole('table', { name: /Versions of/ })).toBeVisible();
			await expectNoSeriousA11yViolations(page);
			await page.goto('/integrations/int_evt_brandA/versions/8');
			await expect(page.getByRole('region', { name: 'Version summary' })).toBeVisible();
			await expectNoSeriousA11yViolations(page);
		});
	}

	test('a version opens from the history by keyboard', async ({ page }) => {
		await signIn(page, { next: '/integrations/int_evt_brandA' });
		const v8 = page.getByRole('link', { name: 'v8', exact: true });
		await expect(v8).toBeVisible();
		await v8.focus();
		await page.keyboard.press('Enter');
		await expect(page).toHaveURL('/integrations/int_evt_brandA/versions/8');
		await expect(page.getByRole('heading', { level: 1 })).toHaveText('int_evt_brandA v8');
		await expect(
			page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link').last()
		).toHaveText('v8');
	});

	test('a viewer sees the page read-only', async ({ page }) => {
		await signIn(page, { role: 'viewer', next: '/integrations/int_evt_brandA' });
		await expect(page.getByText(/Read only\. Publishing, rolling back.*Editor role/)).toBeVisible();
	});

	test('an editor sees no read-only note', async ({ page }) => {
		await signIn(page, { role: 'editor', next: '/integrations/int_evt_brandA' });
		await expect(page.getByRole('region', { name: 'Summary' })).toBeVisible();
		await expect(page.getByText(/Read only\./)).toHaveCount(0);
	});
});

test('768 px: the detail page never scrolls sideways', async ({ page }) => {
	await page.setViewportSize({ width: 768, height: 1024 });
	await signIn(page, { next: '/integrations/int_evt_brandA' });
	await expect(page.getByRole('table', { name: /Versions of/ })).toBeVisible();
	const overflow = await page.evaluate(
		() => document.documentElement.scrollWidth - document.documentElement.clientWidth
	);
	expect(overflow).toBeLessThanOrEqual(0);
});

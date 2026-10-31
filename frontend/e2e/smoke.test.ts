import { expect, test } from '@playwright/test';

test('home page renders the console heading', async ({ page }) => {
	await page.goto('/');
	await expect(page.getByRole('heading', { level: 1 })).toHaveText('Integration Hub console');
	await expect(page).toHaveTitle('Integration Hub console');
});

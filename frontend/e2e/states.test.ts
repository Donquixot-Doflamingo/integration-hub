import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-255: errors thrown by routing use the shared ErrorPanel, inside the shell.
test('an unknown page shows the shared error panel', async ({ page }) => {
	await signIn(page);
	await page.goto('/no-such-section');
	await expect(page.getByRole('heading', { level: 1 })).toHaveText('Not found');
	const alert = page.getByRole('alert');
	await expect(alert).toContainText('NOT_FOUND');
	await expect(alert).toContainText('HTTP 404');
	await expectNoSeriousA11yViolations(page);
	await page.getByRole('link', { name: 'Go to the console home' }).click();
	await expect(page.getByRole('heading', { level: 1 })).toHaveText('Integration Hub console');
});

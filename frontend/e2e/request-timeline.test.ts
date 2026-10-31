import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn, useScenario } from './helpers';

// HUB-264: request timeline on MSW (contract examples).

const ID = 'req_01J9Z3K8X2V7Q4M6N8P0R2S4T6';

test.use({ viewport: { width: 1280, height: 900 } });

test('opens from the explorer; events in order with durations; payload by keyboard', async ({
	page
}) => {
	await signIn(page, { role: 'viewer', next: '/requests' });
	await page.getByRole('link', { name: ID }).click();
	await expect(page).toHaveURL(`/requests/${ID}`);
	await expect(page.getByRole('heading', { level: 1 })).toHaveText(ID);
	const timeline = page.getByRole('region', { name: 'Timeline' });
	const items = timeline.getByRole('listitem');
	await expect(items).toHaveCount(5);
	await expect(items.nth(0)).toContainText('Request received');
	await expect(items.nth(3)).toContainText('+212 ms');
	const show = items.nth(0).getByText(/Show payload/);
	await show.focus();
	await page.keyboard.press('Enter');
	await expect(items.nth(0).getByLabel('payload as JSON')).toContainText('sha256:1c9e');
	const attempts = page.getByRole('table', { name: /Upstream attempts/ });
	await expect(attempts.getByRole('row').nth(1)).toContainText('v7');
	await expect(attempts.getByRole('row').nth(1)).toContainText('clevertap/ct-brandA-in');
	await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link')).toHaveText([
		'Console',
		'Requests',
		ID
	]);
});

test('sealed payloads show a locked placeholder', async ({ page }) => {
	await useScenario(page, 'sealed-request');
	await signIn(page, { role: 'viewer', next: `/requests/${ID}` });
	const first = page.getByRole('region', { name: 'Timeline' }).getByRole('listitem').first();
	await expect(first).toContainText('Sealed payload');
	await expect(first.getByText(/Show payload/)).toHaveCount(0);
});

for (const scheme of ['light', 'dark'] as const) {
	test(`request timeline is axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await useScenario(page, 'sealed-request');
		await signIn(page, { role: 'viewer', next: `/requests/${ID}` });
		await expect(page.getByRole('table', { name: /Upstream attempts/ })).toBeVisible();
		await page
			.getByRole('region', { name: 'Timeline' })
			.getByText(/Show payload/)
			.first()
			.click();
		await expectNoSeriousA11yViolations(page);
	});
}

import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-268: transformer viewer and goldens on MSW (contract examples).

test.use({ viewport: { width: 1280, height: 900 } });

test('list -> transformer: docs link, source, versions, run goldens', async ({ page }) => {
	await signIn(page, { role: 'editor', next: '/transformers' });
	await page.getByLabel('Kind').selectOption({ label: 'Request (canonical to vendor)' });
	await page.getByRole('button', { name: 'Apply' }).click();
	await expect(page).toHaveURL('/transformers?kind=request');
	await page.getByRole('link', { name: 'T1', exact: true }).click();
	await expect(page).toHaveURL('/transformers/T1');
	await expect(page.getByRole('link', { name: /developer\.clevertap\.com/ })).toHaveAttribute(
		'href',
		'https://developer.clevertap.com/docs/upload-events-api'
	);
	await expect(page.getByLabel('T1@7 source (YAML)')).toContainText('transformer: T1');
	const post = page.waitForRequest(
		(r) => r.method() === 'POST' && r.url().includes('?/runGoldens')
	);
	await page.getByRole('button', { name: 'Run goldens for T1@7' }).click();
	expect(new URLSearchParams((await post).postData() ?? '').get('ref')).toBe('T1@7');
	await expect(page.getByRole('status').filter({ hasText: 'All goldens pass' })).toContainText(
		'T1@7: 2 of 2 fixtures pass (12 ms).'
	);
	await page
		.getByRole('region', { name: 'Version history' })
		.getByRole('link', { name: 'T1@6' })
		.click();
	await expect(page).toHaveURL('/transformers/T1/versions/6');
	await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link')).toHaveText([
		'Console',
		'Transformers',
		'T1',
		'v6'
	]);
});

test('viewers see the stored goldens result but cannot run them', async ({ page }) => {
	await signIn(page, { role: 'viewer', next: '/transformers/T1' });
	await expect(page.getByText(/Running goldens needs the Editor role/)).toBeVisible();
	await expect(page.getByRole('button', { name: /Run goldens/ })).toHaveCount(0);
});

for (const scheme of ['light', 'dark'] as const) {
	test(`transformer pages are axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await signIn(page, { role: 'editor', next: '/transformers' });
		await expect(page.getByRole('table')).toBeVisible();
		await expectNoSeriousA11yViolations(page);
		await page.goto('/transformers/T1');
		await page.getByRole('button', { name: 'Run goldens for T1@7' }).click();
		await expect(page.getByText(/fixtures pass/)).toBeVisible();
		await page.getByText('order_placed', { exact: true }).first().click();
		await expectNoSeriousA11yViolations(page);
	});
}

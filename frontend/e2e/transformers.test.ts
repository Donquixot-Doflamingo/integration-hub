import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-268: transformer viewer and goldens; HUB-269: dry-run. On MSW (contract examples).

test.use({ viewport: { width: 1280, height: 900 } });

test('list -> transformer: docs link, source, versions, run goldens', async ({ page }) => {
	await signIn(page, { role: 'editor', next: '/transformers' });
	await page.getByLabel('Kind').selectOption({ label: 'Request (caller input to vendor request)' });
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
	await page.getByRole('button', { name: 'Run samples for T1@7' }).click();
	expect(new URLSearchParams((await post).postData() ?? '').get('ref')).toBe('T1@7');
	await expect(page.getByRole('status').filter({ hasText: 'All samples pass' })).toContainText(
		'T1@7: 2 of 2 samples pass (12 ms).'
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
	await expect(page.getByText(/Running samples needs the Editor role/)).toBeVisible();
	await expect(page.getByRole('button', { name: /Run samples/ })).toHaveCount(0);
});

for (const scheme of ['light', 'dark'] as const) {
	test(`transformer pages are axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await signIn(page, { role: 'editor', next: '/transformers' });
		await expect(page.getByRole('table')).toBeVisible();
		await expectNoSeriousA11yViolations(page);
		await page.goto('/transformers/T1');
		await page.getByRole('button', { name: 'Run samples for T1@7' }).click();
		await expect(page.getByText('T1@7: 2 of 2 samples pass (12 ms)')).toBeVisible();
		await page.getByText('order_placed', { exact: true }).first().click();
		await expectNoSeriousA11yViolations(page);
	});
}

test('dry run: invalid JSON is caught in the browser, the result shows the vendor request', async ({
	page
}) => {
	await signIn(page, { role: 'editor', next: '/transformers/T1' });
	const posts: string[] = [];
	page.on('request', (r) => {
		if (r.method() === 'POST' && r.url().includes('?/dryRun')) posts.push(r.url());
	});
	const input = page.getByLabel(/^Caller input \(JSON body\)/);
	await expect(input).toHaveValue(/"u_48213"/);
	await input.fill('{ "user": { "id": "u_48213" }, }');
	await page.getByRole('button', { name: 'Dry-run T1@7' }).click();
	await expect(page.getByText(/The input is not valid JSON \(line 1, column \d+\)/)).toBeVisible();
	await expect(input).toBeFocused();
	expect(posts).toEqual([]);

	await page.getByRole('button', { name: 'Load' }).click();
	await page.getByRole('button', { name: 'Dry-run T1@7' }).click();
	const result = page.getByRole('region', { name: /Dry run of T1@7/ });
	const request = result.getByRole('group', { name: 'Vendor request' });
	await expect(request).toContainText('POST');
	await expect(request).toContainText('https://in1.api.clevertap.com/1/upload');
	await expect(request.getByRole('row', { name: /X-CleverTap-Passcode/ })).toContainText('***');
	await expect(request.getByLabel('Vendor request body')).toContainText(
		'"evtName": "order_placed"'
	);
	expect(posts).toHaveLength(1);
	await expectNoSeriousA11yViolations(page);
});

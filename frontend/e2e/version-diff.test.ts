import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-258: version diff on MSW (contract example: v7 -> v8, incl. an operation's input field).

test.use({ viewport: { width: 1280, height: 900 } });

test('pick two versions in the history and compare them', async ({ page }) => {
	await signIn(page, { next: '/integrations/int_evt_brandA' });
	const history = page.getByRole('form', { name: 'Compare versions' });
	await expect(history.getByRole('radio', { name: 'Compare from v7' })).toBeChecked();
	await history.getByRole('radio', { name: 'Compare to v8' }).check();
	const diffCall = page.waitForRequest((r) =>
		r.url().includes('/admin/v1/integrations/int_evt_brandA/diff?')
	);
	await history.getByRole('button', { name: 'Compare selected versions' }).click();
	await expect(page).toHaveURL(/\/integrations\/int_evt_brandA\/diff\?/);
	// A native GET form: parameter order follows the radios' order in the table.
	expect(Object.fromEntries(new URL(page.url()).searchParams)).toEqual({ from: '7', to: '8' });
	expect(Object.fromEntries(new URL((await diffCall).url()).searchParams)).toEqual({
		from: '7',
		to: '8'
	});

	const split = page.getByRole('region', { name: 'Side-by-side diff' });
	await expect(split.getByRole('columnheader')).toHaveText(['v7', 'v8']);
	await expect(split).toContainText('weight: 100');
	await expect(split).toContainText('weight: 90');
	await expect(page.getByText('3 paths differ (2 added, 1 changed)')).toBeVisible();
	await expect(
		page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link').last()
	).toHaveText('Compare');
});

test('a row link compares a version with the one it was based on', async ({ page }) => {
	await signIn(page, { next: '/integrations/int_evt_brandA' });
	await page.getByRole('link', { name: 'vs v7' }).click();
	await expect(page).toHaveURL('/integrations/int_evt_brandA/diff?from=7&to=8');
	await expect(page.getByRole('region', { name: 'Side-by-side diff' })).toBeVisible();
});

test('switches to the path list by keyboard, and the URL keeps the view', async ({ page }) => {
	await signIn(page, { next: '/integrations/int_evt_brandA/diff?from=7&to=8' });
	const views = page.getByRole('navigation', { name: 'Diff view' });
	await expect(views.getByRole('link', { name: 'Side by side' })).toHaveAttribute(
		'aria-current',
		'page'
	);
	await views.getByRole('link', { name: 'Changed paths' }).focus();
	await page.keyboard.press('Enter');
	await expect(page).toHaveURL('/integrations/int_evt_brandA/diff?from=7&to=8&view=paths');
	const paths = page.getByRole('region', { name: 'Changed paths' });
	await expect(paths.getByRole('rowheader')).toHaveCount(3);
	await expect(paths.getByRole('row').nth(1)).toContainText('targets[0].weight');
	await expect(paths.getByRole('row').nth(1)).toContainText('Changed');
	await expect(paths.getByRole('row').nth(2)).toContainText('Added');
	await expect(paths.getByRole('row').nth(3)).toContainText(
		'operations.default.request_schema.body.properties.context.properties.channel'
	);
	await expect(paths.getByRole('row').nth(3)).toContainText('Added');
	await page.reload();
	await expect(page.getByRole('region', { name: 'Changed paths' })).toBeVisible();
});

test('without a pair, the diff page opens on newest vs active', async ({ page }) => {
	await signIn(page, { next: '/integrations/int_evt_brandA/diff' });
	await expect(page).toHaveURL('/integrations/int_evt_brandA/diff?from=7&to=8');
	await expect(page.getByLabel('From (old)')).toHaveValue('7');
	await expect(page.getByLabel('To (new)')).toHaveValue('8');
});

for (const scheme of ['light', 'dark'] as const) {
	test(`both diff views are axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await signIn(page, { next: '/integrations/int_evt_brandA/diff?from=7&to=8' });
		await expect(page.getByRole('region', { name: 'Side-by-side diff' })).toBeVisible();
		await expectNoSeriousA11yViolations(page);
		await page.goto('/integrations/int_evt_brandA/diff?from=7&to=8&view=paths');
		await expect(page.getByRole('region', { name: 'Changed paths' })).toBeVisible();
		await expectNoSeriousA11yViolations(page);
	});
}

import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-266: DLQ list with URL filters and the entry detail, on MSW (contract examples).

const ID = 'req_01J9Z3M1A5B7C9D1E3F5G7H9JK';

test.use({ viewport: { width: 1280, height: 900 } });

test('filters go to the URL as chips; an entry shows reason and attempt history', async ({
	page
}) => {
	await signIn(page, { role: 'viewer', next: '/dlq' });
	await page.getByLabel('Integration').fill('int_evt_brandA');
	await page.getByLabel(/^Reason/).fill('upstream_rejected');
	await page.getByLabel('Replayed').selectOption({ label: 'Not replayed yet' });
	await page.getByRole('button', { name: 'Apply' }).click();
	await expect(page).toHaveURL(
		'/dlq?integration_id=int_evt_brandA&reason=UPSTREAM_REJECTED&replayed=false'
	);
	const chips = page.getByRole('list', { name: 'Active filters' });
	await expect(chips.getByRole('link')).toHaveCount(3);
	// Reload keeps the view (filters live in the URL).
	await page.reload();
	await expect(page.getByLabel(/^Reason/)).toHaveValue('UPSTREAM_REJECTED');

	await page.getByRole('link', { name: ID }).click();
	await expect(page).toHaveURL(`/dlq/${ID}`);
	await expect(page.getByRole('group', { name: 'Reason' })).toContainText(
		'event name must not contain spaces'
	);
	const history = page.getByRole('table', { name: /Upstream attempts/ });
	await expect(history.getByRole('row').nth(1)).toContainText('UPSTREAM_REJECTED');
	await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link')).toHaveText([
		'Console',
		'DLQ',
		ID
	]);
});

test('the health board links to the DLQ filtered by account', async ({ page }) => {
	await signIn(page, { role: 'viewer', next: '/health' });
	await page.getByRole('link', { name: /dead letter for ct-brandA-in/ }).click();
	await expect(page).toHaveURL('/dlq?account_id=ct-brandA-in');
	await expect(page.getByRole('textbox', { name: 'Account' })).toHaveValue('ct-brandA-in');
});

for (const scheme of ['light', 'dark'] as const) {
	test(`DLQ list and entry are axe-clean in ${scheme}`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: scheme });
		await signIn(page, { role: 'viewer', next: '/dlq?replayed=false' });
		await expect(page.getByRole('table')).toBeVisible();
		await expectNoSeriousA11yViolations(page);
		await page.goto(`/dlq/${ID}`);
		await expect(page.getByRole('group', { name: 'Reason' })).toBeVisible();
		await page.getByText(/^Show vendor request/).click();
		await expectNoSeriousA11yViolations(page);
	});
}

import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-266: DLQ list with URL filters and the entry detail; HUB-267: replay and purge by filter.
// On MSW (contract examples; the bulk mocks echo `dry_run`).

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

test('replay by filter: exact count first, reason, change log id', async ({ page }) => {
	await signIn(page, { role: 'editor', next: '/dlq' });
	await page.getByLabel('Integration').fill('int_evt_brandA');
	await page.getByLabel('Replayed').selectOption({ label: 'Not replayed yet' });
	await page.getByRole('button', { name: 'Apply' }).click();
	await expect(page).toHaveURL('/dlq?integration_id=int_evt_brandA&replayed=false');
	await expect(page.getByRole('button', { name: /Purge matching/ })).toHaveCount(0);
	const count = page.waitForRequest(
		(r) => r.method() === 'POST' && r.url().includes('?/countReplay')
	);
	await page.getByRole('button', { name: /Replay matching/ }).click();
	const counted = new URLSearchParams((await count).postData() ?? '');
	expect([counted.get('filter.integration_id'), counted.get('filter.replayed')]).toEqual([
		'int_evt_brandA',
		'false'
	]);
	const dialog = page.getByRole('dialog', { name: 'Replay 42 dead letters' });
	await expect(dialog).toContainText('Moves the 42 dead letters matching these filters');
	const confirm = dialog.getByRole('button', { name: 'Replay' });
	await expect(confirm).toBeDisabled();
	await dialog.getByLabel(/Reason/).fill('Fixed T1 event-name sanitising (T1@8)');
	const post = page.waitForRequest((r) => r.method() === 'POST' && r.url().includes('?/replay'));
	await confirm.click();
	const sent = new URLSearchParams((await post).postData() ?? '');
	expect([sent.get('reason'), sent.get('expected'), sent.get('filter.integration_id')]).toEqual([
		'Fixed T1 event-name sanitising (T1@8)',
		'42',
		'int_evt_brandA'
	]);
	const notice = page.getByRole('status').filter({ hasText: 'Replayed 42 dead letters' });
	await expect(notice).toContainText('chg_01J9Z4B2C3D4E5F6G7H8J9K0NM');
	await expect(notice.getByRole('link', { name: 'Show the replayed entries' })).toHaveAttribute(
		'href',
		'/dlq?integration_id=int_evt_brandA&replayed=true'
	);
});

test('purge needs an admin, the reason and the count typed', async ({ page }) => {
	await signIn(page, { role: 'admin', next: '/dlq?reason=UPSTREAM_REJECTED' });
	const trigger = page.getByRole('button', { name: /Purge matching/ });
	await trigger.click();
	const dialog = page.getByRole('dialog', { name: 'Purge 1 dead letter' });
	await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
	await expectNoSeriousA11yViolations(page);
	const confirm = dialog.getByRole('button', { name: 'Purge' });
	await dialog.getByLabel(/Reason/).fill('Test traffic from a misconfigured staging client');
	await expect(confirm).toBeDisabled();
	await dialog.getByLabel('Type 1 to confirm').fill('1');
	await confirm.click();
	await expect(page.getByRole('status').filter({ hasText: 'Purged 1 dead letter' })).toContainText(
		'chg_01J9Z4B2C3D4E5F6G7H8J9K0NP'
	);
});

test('viewers get no bulk actions', async ({ page }) => {
	await signIn(page, { role: 'viewer', next: '/dlq' });
	await expect(page.getByRole('table')).toBeVisible();
	await expect(page.getByRole('button', { name: /matching/ })).toHaveCount(0);
});

test('Escape closes the replay dialog and returns focus', async ({ page }) => {
	await signIn(page, { role: 'editor', next: '/dlq' });
	const trigger = page.getByRole('button', { name: /Replay matching/ });
	await trigger.focus();
	await page.keyboard.press('Enter');
	await expect(page.getByRole('dialog')).toBeVisible();
	await page.keyboard.press('Escape');
	await expect(page.getByRole('dialog')).toBeHidden();
	await expect(trigger).toBeFocused();
});

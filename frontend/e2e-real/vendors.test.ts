import { expect, test } from '@playwright/test';
import { changeIdOf, expectNoSeriousA11yViolations, shot, signIn, uid } from './helpers';

// HUB-290 against the real stack: create a vendor, add an endpoint and a new endpoint version,
// edit the vendor with If-Match (and lose a race with a second tab), roles.

const VENDOR = uid('e2e-vendor');

test.describe.configure({ mode: 'serial' });

test('admin creates a vendor; field and API errors land on the form', async ({ page }) => {
	await signIn(page, { role: 'admin', next: '/vendors' });
	await shot(page, 'vendors-list');
	await page.getByRole('link', { name: 'Create vendor' }).click();
	await expect(page).toHaveURL('/vendors/new');

	// The console's own checks: nothing is sent, the problem sits on its field.
	await page.getByLabel(/^Vendor id/).fill('x');
	await page.getByLabel(/^Display name/).fill('E2E vendor');
	await page.getByLabel('Region code').fill('in');
	await page.getByLabel('Base URL').fill('not a url');
	await page.getByLabel(/^Reason/).fill('Onboard the e2e vendor');
	await page.getByRole('button', { name: 'Create vendor' }).click();
	await expect(page.getByLabel(/^Vendor id/)).toHaveAttribute('aria-invalid', 'true');
	await expect(page.getByText('3 to 64 characters', { exact: false }).first()).toBeVisible();
	await expect(
		page.getByText('Region "in": enter an absolute http(s) base URL.').first()
	).toBeVisible();

	await page.getByLabel(/^Vendor id/).fill('clevertap'); // exists in the dev seed: 409 from the API
	await page.getByLabel('Base URL').fill('https://api.e2e.example.com');
	await page.getByRole('button', { name: 'Add region' }).click();
	await page.getByLabel('Region code').nth(1).fill('us');
	await page.getByLabel('Base URL').nth(1).fill('https://us.api.e2e.example.com');
	await page.getByLabel('Rate limit per account (rps)').fill('25');
	await page.getByLabel('Window (ms)').fill('30000');
	await page.getByRole('button', { name: 'Create vendor' }).click();
	const conflict = page.getByRole('alert').filter({ hasText: 'CONFLICT' });
	await expect(conflict).toContainText('clevertap');
	await expect(conflict).toContainText(/req_[0-9A-Z]{26}/);
	await shot(page, 'vendor-new');

	await page.getByLabel(/^Vendor id/).fill(VENDOR);
	await page.getByRole('button', { name: 'Create vendor' }).click();
	await expect(page).toHaveURL(`/vendors/${VENDOR}`);
	await changeIdOf(page, `Created vendor ${VENDOR}`);
	await expect(page.getByRole('heading', { level: 1 })).toHaveText(VENDOR);
	await expect(page.getByRole('region', { name: 'Region base URLs' })).toContainText(
		'https://us.api.e2e.example.com'
	);
	await expectNoSeriousA11yViolations(page);
});

test('admin adds an endpoint, then a new version of it (versions are immutable)', async ({
	page
}) => {
	await signIn(page, { role: 'admin', next: `/vendors/${VENDOR}` });
	// Endpoints are created on their own page with the request they accept (HUB-436, ADR-0023);
	// this one declares nothing besides method and path.
	await page.getByRole('link', { name: 'Add endpoint' }).click();
	await expect(page).toHaveURL(`/vendors/${VENDOR}/endpoints/new`);
	await page.getByLabel('Name (required)').fill('send_event');
	await page.getByLabel('Path (required)').fill('/v1/events');
	await page.getByRole('checkbox', { name: /^Idempotent/ }).check();
	await page.getByLabel(/^Reason/).fill('Events endpoint for the e2e vendor');
	await page.getByRole('button', { name: 'Add endpoint' }).click();
	await expect(page).toHaveURL(`/vendors/${VENDOR}/endpoints/send_event@1`);
	await changeIdOf(page, 'Created endpoint send_event@1');

	await page.getByRole('link', { name: 'New version (send_event@2)' }).click();
	await expect(page).toHaveURL(`/vendors/${VENDOR}/endpoints/new?from=send_event`);
	await expect(page.getByLabel('Name (required)')).toHaveValue('send_event');
	await expect(page.getByLabel('Name (required)')).toHaveAttribute('readonly', '');
	await expect(page.getByLabel('Path (required)')).toHaveValue('/v1/events');
	await page.getByLabel('Path (required)').fill('/v2/events');
	await page.getByLabel('Timeout (ms)', { exact: true }).fill('2500');
	await page.getByLabel(/^Reason/).fill('v2 path of the events API');
	await page.getByRole('button', { name: 'Create send_event@2' }).click();
	await expect(page).toHaveURL(`/vendors/${VENDOR}/endpoints/send_event@2`);
	await changeIdOf(page, 'Created endpoint send_event@2');

	await page.goto(`/vendors/${VENDOR}`);
	const table = page.getByRole('region', { name: 'Endpoint versions' });
	await expect(table.getByRole('row', { name: /send_event@2/ })).toContainText('Latest');
	await expect(table.getByRole('row', { name: /send_event@1/ })).toContainText('/v1/events');
	await shot(page, 'vendor-detail');

	await table.getByRole('link', { name: 'send_event@1' }).click();
	await expect(page.getByRole('note')).toContainText('A newer version exists: send_event@2');
	await page.getByRole('link', { name: 'send_event@2' }).click();
	await expect(page.getByRole('link', { name: 'New version (send_event@3)' })).toBeVisible();
	await shot(page, 'vendor-endpoint');
	await expectNoSeriousA11yViolations(page);
});

test('edit sends If-Match; a stale tab gets a reload prompt instead of overwriting', async ({
	page,
	context
}) => {
	await signIn(page, { role: 'admin', next: `/vendors/${VENDOR}/edit` });
	const other = await context.newPage();
	await other.goto(`/vendors/${VENDOR}/edit`);
	await expect(other.getByLabel(/^Display name/)).toHaveValue('E2E vendor');

	await page.getByLabel(/^Display name/).fill('E2E vendor renamed');
	await page.getByLabel('Probe endpoint').fill('send_event@2');
	await page.getByLabel(/^Reason/).fill('Rename and set the health probe');
	const put = page.waitForRequest((r) => r.method() === 'POST' && r.url().includes('?/update'));
	await shot(page, 'vendor-edit');
	await page.getByRole('button', { name: 'Save vendor' }).click();
	expect(new URLSearchParams((await put).postData() ?? '').get('if_match')).toMatch(/^".+"$/);
	await expect(page).toHaveURL(`/vendors/${VENDOR}`);
	await changeIdOf(page, `Saved vendor ${VENDOR}: revision 2`);
	await expect(page.getByText('E2E vendor renamed')).toBeVisible();

	await other.getByLabel(/^Display name/).fill('Stale rename');
	await other.getByLabel(/^Reason/).fill('This tab loaded revision 1');
	await other.getByRole('button', { name: 'Save vendor' }).click();
	await expect(other.getByRole('alert').filter({ hasText: 'Someone else changed' })).toBeVisible();
	await expect(other.getByLabel(/^Display name/)).toHaveValue('Stale rename');
	await other.close();
});

test('editors and viewers read vendors without change controls; tenant viewers see none', async ({
	page
}) => {
	for (const role of ['editor', 'viewer'] as const) {
		await signIn(page, { role, next: `/vendors/${VENDOR}` });
		await expect(page.getByRole('heading', { level: 1 })).toHaveText(VENDOR);
		await expect(page.getByRole('region', { name: 'Endpoint versions' })).toBeVisible();
		await expect(page.getByRole('link', { name: 'Edit vendor' })).toHaveCount(0);
		await expect(page.getByRole('link', { name: 'Add endpoint' })).toHaveCount(0);
		await expect(page.getByRole('link', { name: /New version/ })).toHaveCount(0);
		await expect(page.getByRole('note')).toContainText('needs the Admin role');
		await page.goto('/vendors');
		await expect(page.getByRole('link', { name: 'Create vendor' })).toHaveCount(0);
		await page.context().clearCookies();
	}
	await signIn(page, { role: 'tenant_viewer', tenants: 'orders-service', next: '/vendors' });
	await expect(page.getByRole('note')).toContainText('tenant viewers cannot see them');
	await expect(page.getByRole('table')).toHaveCount(0);
});

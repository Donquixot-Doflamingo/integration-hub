import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-290..293: every authoring page renders on the contract mocks and is axe-clean (no serious or
// critical violations) in light and dark. The flows themselves run against the real stack in
// e2e-real/ (npm run test:e2e:real).

const PAGES: { path: string; heading: string }[] = [
	{ path: '/vendors', heading: 'Vendors' },
	{ path: '/vendors/new', heading: 'Create vendor' },
	{ path: '/vendors/clevertap', heading: 'clevertap' },
	{ path: '/vendors/clevertap/edit', heading: 'Edit clevertap' },
	{ path: '/vendors/clevertap/endpoints/upload@1', heading: 'clevertap.upload@1' },
	{ path: '/vendors/clevertap/endpoints/new', heading: 'Add an endpoint to clevertap' },
	{
		path: '/vendors/clevertap/endpoints/new?from=upload',
		heading: 'New version of clevertap.upload'
	},
	{ path: '/accounts', heading: 'Accounts' },
	{ path: '/accounts/new', heading: 'Create account' },
	{ path: '/accounts/ct-brandA-in', heading: 'ct-brandA-in' },
	{ path: '/integrations/new', heading: 'Create integration' },
	{ path: '/integrations/int_evt_brandA/publish', heading: 'Publish int_evt_brandA' },
	{ path: '/transformers/new', heading: 'New transformer' },
	{ path: '/transformers/T1/new-version', heading: 'New version of T1' }
];

for (const theme of ['light', 'dark'] as const) {
	test(`authoring pages are axe-clean (${theme})`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: theme });
		await signIn(page, { role: 'admin' });
		for (const p of PAGES) {
			await page.goto(p.path);
			await expect(page.locator('main h1')).toHaveText(p.heading);
			await expectNoSeriousA11yViolations(page);
		}
	});
}

test('dialogs of the authoring pages are axe-clean', async ({ page }) => {
	await signIn(page, { role: 'admin', next: '/accounts/ct-brandA-in' });
	await page.getByRole('button', { name: 'Add next credential' }).click();
	await expect(page.getByRole('dialog').getByLabel('Secret value')).toHaveAttribute(
		'type',
		'password'
	);
	await expectNoSeriousA11yViolations(page);
});

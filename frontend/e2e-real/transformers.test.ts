import { expect, test } from '@playwright/test';
import { changeIdOf, expectNoSeriousA11yViolations, RUN, shot, signIn } from './helpers';

// HUB-293 against the real stack: the next version of T1 (failing goldens block "Create", diffs
// per fixture, masked dry run, then passing goldens and create with a reason), and a new
// transformer id. Roles.

const NEW_ID = `T-E2E-${RUN}`;

test.describe.configure({ mode: 'serial' });

test('next version of T1: goldens gate create, diffs per case, masked dry run', async ({
	page
}) => {
	await signIn(page, { role: 'editor', next: '/transformers/T1' });
	const latest = Number(
		/v(\d+)/.exec((await page.getByRole('link', { name: /^New version/ }).textContent()) ?? '')?.[1]
	);
	await page.getByRole('link', { name: /^New version/ }).click();
	await expect(page).toHaveURL('/transformers/T1/new-version');
	await expect(page.getByLabel(/^Source/)).toHaveValue(/kind: request/);
	const create = page.getByRole('button', { name: `Create T1@${latest}` });
	await expect(create).toBeDisabled();
	await expect(page.getByText('Run the goldens on this draft first')).toBeVisible();

	// Break one fixture: the goldens fail with a diff for that case, and Create stays locked.
	const first = page.getByRole('listitem', { name: /^Fixture 1/ });
	const expected = first.getByLabel('Expected vendor request');
	const original = await expected.inputValue();
	await expected.fill(original.replace('"/1/upload"', '"/1/wrong"'));
	await page.getByRole('button', { name: 'Run goldens on this draft' }).click();
	const summary = page.getByRole('status').filter({ hasText: /fixtures? pass/ });
	await expect(summary).toContainText('Goldens fail');
	await expect(page.getByText('/1/wrong').first()).toBeVisible();
	await expect(create).toBeDisabled();
	await expect(page.getByText(/goldens fail\. Fix the source or the fixtures/)).toBeVisible();

	// Dry run of the draft: the vendor request with credentials masked as the API returns them.
	await page
		.getByLabel('Account (optional: its config, credentials redacted)')
		.fill('ct-brandA-in');
	await page.getByRole('button', { name: 'Dry run this draft' }).click();
	const dry = page.getByRole('region', { name: 'Dry run', exact: true });
	await expect(dry.getByText(/Dry run of this draft/)).toBeVisible();
	await expect(dry.getByRole('row', { name: /X-CleverTap-Passcode/ })).toContainText('***');
	await shot(page, 'transformer-new-version');

	// Fix it: goldens pass on this exact draft, Create unlocks; the Hub runs them again.
	await expected.fill(original);
	await expect(page.getByText('The draft changed since this run')).toBeVisible();
	await page.getByRole('button', { name: 'Run goldens on this draft' }).click();
	await expect(summary).toContainText('All goldens pass');
	await expect(create).toBeEnabled();
	await create.click();
	const dialog = page.getByRole('dialog', { name: `Create T1@${latest}` });
	await expect(dialog.getByRole('button', { name: `Create T1@${latest}` })).toBeDisabled();
	await dialog.getByLabel(/^Reason/).fill('Same mapping, e2e version');
	await dialog.getByRole('button', { name: `Create T1@${latest}` }).click();
	await expect(page).toHaveURL(`/transformers/T1/versions/${latest}`);
	await changeIdOf(page, `Created T1@${latest}`);
	await expectNoSeriousA11yViolations(page);
});

test('a new transformer id from the console, version 1', async ({ page }) => {
	await signIn(page, { role: 'editor', next: '/transformers' });
	// Start from T1's source and fixtures (read through the console's admin proxy).
	const t1 = (await (await page.request.get('/admin/v1/transformers/T1')).json()) as {
		latest: { yaml: string; fixtures: Record<string, unknown>[] };
	};
	await page.getByRole('link', { name: 'New transformer' }).click();
	await expect(page).toHaveURL('/transformers/new');
	await page.getByLabel(/^Transformer id/).fill(NEW_ID);
	await page.getByLabel('Kind').selectOption('request');
	await page.getByLabel('Capability').selectOption('events.track@v1');
	await page.getByLabel('Vendor endpoint').fill('clevertap.upload@1');
	await page
		.getByLabel(/^Vendor documentation/)
		.fill('https://developer.clevertap.com/docs/upload-events-api');
	await page
		.getByLabel(/^Source/)
		.fill(
			t1.latest.yaml
				.replace(/^transformer: T1$/m, `transformer: ${NEW_ID}`)
				.replace(/^version: \d+$/m, 'version: 1')
		);
	const f = t1.latest.fixtures[0]!;
	const fixture = page.getByRole('listitem', { name: /^Fixture 1/ });
	await fixture.getByLabel('Name').fill(String(f.name));
	await fixture.getByLabel('Canonical input').fill(JSON.stringify(f.input, null, 2));
	await fixture
		.getByLabel('Expected vendor request')
		.fill(JSON.stringify(f.expected_request, null, 2));
	await page.getByRole('button', { name: 'Run goldens on this draft' }).click();
	await expect(page.getByRole('status').filter({ hasText: /fixtures? pass/ })).toContainText(
		'All goldens pass'
	);
	await shot(page, 'transformer-new');
	await page.getByRole('button', { name: `Create ${NEW_ID}@1` }).click();
	const dialog = page.getByRole('dialog', { name: `Create ${NEW_ID}@1` });
	await dialog.getByLabel(/^Reason/).fill('New transformer created from the console');
	await dialog.getByRole('button', { name: `Create ${NEW_ID}@1` }).click();
	await expect(page).toHaveURL(`/transformers/${NEW_ID}/versions/1`);
	await changeIdOf(page, `Created ${NEW_ID}@1`);
});

test('viewers see transformers without authoring controls', async ({ page }) => {
	await signIn(page, { role: 'viewer', next: '/transformers' });
	await expect(page.getByRole('link', { name: 'New transformer' })).toHaveCount(0);
	await page.goto('/transformers/T1');
	await expect(page.getByRole('link', { name: /^New version/ })).toHaveCount(0);
	await page.goto('/transformers/T1/new-version');
	await expect(page.getByRole('note')).toContainText('needs the Editor role');
	await expect(page.getByRole('button', { name: /Run goldens/ })).toHaveCount(0);
});

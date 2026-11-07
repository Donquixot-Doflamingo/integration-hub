import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test, type Page } from '@playwright/test';
import { parse } from 'yaml';
import { changeIdOf, expectNoSeriousA11yViolations, shot, signIn, uid } from './helpers';

// HUB-292 against the real stack: create an integration, build its first draft in the form editor
// (its operation's request schema, validation report errors on their fields), publish with a
// reason, change the weights in a new draft, compare against the active version, publish again,
// then roll back with a reason.

const INT = uid('int-e2e');
const REASON = 'Onboard the e2e integration';

test.describe.configure({ mode: 'serial' });

/**
 * The request body schema of the dev registry's events integration (backend/config,
 * int_evt_brandA v2): what T1@7 reads (ADR-0023, HUB-441).
 */
function eventsBodySchema(): Record<string, unknown> {
	const file = join(
		import.meta.dirname,
		'..',
		'..',
		'backend',
		'config',
		'integrations',
		'int_evt_brandA',
		'versions',
		'2.yaml'
	);
	const version = parse(readFileSync(file, 'utf8')) as {
		operations: { default: { request_schema: { body: Record<string, unknown> } } };
	};
	return version.operations.default.request_schema.body;
}

/** Describes operation `default` and sets its request body schema in the JSON Schema view. */
async function fillDefaultOperation(page: Page) {
	const op = page.getByRole('listitem', { name: 'Operation default' });
	await op.getByLabel('Description (shown to callers)').fill('Track a single event for a user.');
	await op.getByRole('checkbox', { name: /^Idempotent/ }).check();
	const body = op.getByRole('group', { name: 'Request body (required)' });
	await body.getByRole('button', { name: 'Edit as JSON Schema' }).click();
	await body
		.getByLabel('Request body as JSON Schema')
		.fill(JSON.stringify(eventsBodySchema(), null, 2));
	await body.getByRole('button', { name: 'Fields', exact: true }).click();
	await expect(body.getByRole('checkbox', { name: 'user required' })).toBeChecked();
}

/** Fills one target of the form editor. */
async function fillTarget(page: Page, n: number, t: { account: string; weight: string }) {
	const target = page.getByRole('listitem', { name: new RegExp(`^Target ${n}`) });
	await target.getByLabel('Vendor').fill('clevertap');
	await target.getByLabel('Account').fill(t.account);
	await target.getByLabel('Weight (%)').fill(t.weight);
	await target.getByLabel('Request transformer', { exact: true }).fill('T1@7');
	await target.getByLabel('Response transformer (optional)', { exact: true }).fill('R1@2');
	return target;
}

test('editor creates an integration and lands on its first draft', async ({ page }) => {
	await signIn(page, { role: 'editor', next: '/integrations' });
	await page.getByRole('link', { name: 'Create integration' }).click();
	await expect(page).toHaveURL('/integrations/new');
	await page.getByLabel(/^Integration id/).fill(INT);
	await page.getByLabel(/^Owner tenant/).selectOption('orders-service');
	await page.getByLabel('Description').fill('E2E events integration');
	await page.getByLabel(/^Reason/).fill(REASON);
	await shot(page, 'integration-new');
	await page.getByRole('button', { name: 'Create integration' }).click();
	await expect(page).toHaveURL(`/integrations/${INT}/publish`);
	await changeIdOf(page, `Created integration ${INT}`);
	await expect(page.getByRole('button', { name: 'Form' })).toHaveAttribute('aria-pressed', 'true');
	await expectNoSeriousA11yViolations(page);
});

test('form editor: checks on the fields, validation report inline, publish with reason', async ({
	page
}) => {
	await signIn(page, { role: 'editor', next: `/integrations/${INT}/publish` });
	// Saving an empty draft is refused by the console's own checks, shown on the fields.
	await page.getByRole('button', { name: 'Add target' }).click();
	await page.getByLabel(/^Why this version/).fill('First version of the e2e integration');
	await page.getByRole('button', { name: 'Create draft' }).click();
	const t1 = page.getByRole('listitem', { name: /^Target 1/ });
	await expect(t1.getByLabel('Vendor')).toHaveAttribute('aria-invalid', 'true');
	await expect(t1.getByText('Pick the request transformer.')).toBeVisible();

	await fillDefaultOperation(page);
	await fillTarget(page, 1, { account: 'ct-brandA-in', weight: '100' });
	// Wait mode without a polling path or webhook: the Hub's publish check fails at /delivery.
	await page.getByLabel('Mode').selectOption('wait');
	await page.getByRole('button', { name: 'Create draft' }).click();
	await changeIdOf(page, 'Saved draft v1');
	const delivery = page.getByRole('group', { name: 'Delivery' });
	await expect(delivery.locator('.fk-error')).toContainText('needs polling: true or a webhook');
	await expect(page.getByRole('button', { name: 'Publish v1' })).toBeDisabled();
	await shot(page, 'integration-draft-form');

	await page.getByLabel('Mode').selectOption('sync');
	await page.getByLabel(/^Why you are changing the draft/).fill('Sync delivery for the e2e flow');
	await page.getByRole('button', { name: 'Save draft v1' }).click();
	await changeIdOf(page, 'All publish checks pass');

	// The YAML tab shows the same draft.
	await page.getByRole('button', { name: 'YAML' }).click();
	await expect(page.getByLabel(/^Spec/)).toHaveValue(/account_id: ct-brandA-in/);
	await expect(page.getByLabel(/^Spec/)).toHaveValue(/mode: sync/);
	await expect(page.getByLabel(/^Spec/)).toHaveValue(/validate_input: true/);
	await page.getByRole('button', { name: 'Form' }).click();

	await page.getByRole('button', { name: 'Publish v1' }).click();
	const dialog = page.getByRole('dialog', { name: `Publish ${INT} v1` });
	await expect(dialog.getByRole('button', { name: 'Publish v1' })).toBeDisabled();
	await dialog.getByLabel(/^Reason/).fill('First version: brand A workspace');
	await dialog.getByRole('button', { name: 'Publish v1' }).click();
	await changeIdOf(page, 'Published v1');
});

test('weights in a new draft: total checked live, diff against the active version, publish', async ({
	page
}) => {
	await signIn(page, { role: 'editor', next: `/integrations/${INT}/publish` });
	await page.getByRole('button', { name: 'Add target' }).click();
	await fillTarget(page, 2, { account: 'ct-brandB-us', weight: '50' });
	const total = page.getByRole('group', { name: 'Targets' }).getByRole('status');
	await expect(total).toHaveText('Weights total 150: they must total 100.');
	await page
		.getByRole('listitem', { name: /^Target 1/ })
		.getByLabel('Weight (%)')
		.fill('50');
	await expect(total).toHaveText('Weights total 100.');
	await page.getByLabel(/^Why this version/).fill('Canary 50% to the brand B workspace');
	await page.getByRole('button', { name: 'Create draft' }).click();
	await changeIdOf(page, 'Saved draft v2');
	await page.reload();
	const diff = page.getByRole('region', { name: 'Changes against the active version' });
	await expect(diff).toContainText('v1 to v2');
	await expect(diff).toContainText('ct-brandB-us');
	await shot(page, 'integration-draft-diff');
	await page.getByRole('button', { name: 'Publish v2' }).click();
	const dialog = page.getByRole('dialog', { name: `Publish ${INT} v2` });
	await expect(dialog).toContainText('replacing v1');
	await dialog.getByLabel(/^Reason/).fill('Canary 50/50 between brand A and B');
	await dialog.getByRole('button', { name: 'Publish v2' }).click();
	await changeIdOf(page, 'Published v2');
});

test('rollback to v1 with a reason creates v3', async ({ page }) => {
	await signIn(page, { role: 'editor', next: `/integrations/${INT}` });
	await page.getByRole('button', { name: 'Roll back to v1' }).click();
	const dialog = page.getByRole('dialog', { name: `Roll back ${INT} to v1` });
	await expect(dialog.getByRole('button', { name: 'Roll back' })).toBeDisabled();
	await dialog.getByLabel(/^Reason/).fill('Brand B workspace returns errors');
	await dialog.getByRole('button', { name: 'Roll back' }).click();
	await changeIdOf(page, 'Rolled back');
	await expect(page.getByRole('status').filter({ hasText: 'Rolled back' })).toContainText(
		'v3 is now active, a copy of v1'
	);
});

test('viewers and tenant viewers cannot create or edit integrations', async ({ page }) => {
	await signIn(page, { role: 'viewer', next: '/integrations' });
	await expect(page.getByRole('heading', { level: 1 })).toHaveText('Integrations');
	await expect(page.getByRole('link', { name: 'Create integration' })).toHaveCount(0);
	await page.goto(`/integrations/${INT}/publish`);
	await expect(page.getByRole('note')).toContainText('needs the Editor role');
	await expect(page.getByRole('button', { name: 'Form' })).toHaveCount(0);
	await page.context().clearCookies();
	await signIn(page, {
		role: 'tenant_viewer',
		tenants: 'orders-service',
		next: '/integrations/new'
	});
	await expect(page.getByRole('note')).toContainText('Creating integrations needs the Editor role');
});

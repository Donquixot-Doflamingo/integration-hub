import { expect, test, type Page } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn, useScenario } from './helpers';

// HUB-436 (ADR-0023): a third-party API onboarded entirely from the panel, no code. An agify-like
// public API: vendor with auth none, endpoint GET / with a required query `name`, an account
// without credentials, a request transformer proven by a sample, a response transformer keeping
// `age`, and an integration whose body `{firstName, lastName}` is required, then publish.
// On MSW, scenario `onboarding` (src/lib/api/mocks/onboarding.ts): create calls answer with what
// was sent; the mocks keep no state, so each step asserts what the panel sent.

test.use({ viewport: { width: 1280, height: 900 } });

/** The form fields the next form action posts (`?/<action>`), once the given click sends it. */
async function posted(page: Page, action: string, send: () => Promise<void>) {
	const req = page.waitForRequest(
		(r) => r.method() === 'POST' && new URL(r.url()).search === `?/${action}`
	);
	await send();
	return new URLSearchParams((await req).postData() ?? '');
}

const REASON = 'Onboard the agify age estimate (HUB-436)';

test('onboard an agify-like public API from scratch and publish it', async ({ page }) => {
	test.setTimeout(120_000);
	await useScenario(page, 'onboarding');
	await signIn(page, { role: 'admin', next: '/vendors/new' });

	// 1. Vendor: a public API, auth scheme none.
	await page.getByLabel('Vendor id (required)').fill('agify');
	await page.getByLabel('Display name (required)').fill('Agify');
	await page
		.getByLabel('Auth scheme (required)')
		.selectOption({ label: 'None (public API, no credentials)' });
	await page.getByLabel('Region code').fill('global');
	await page.getByLabel('Base URL').fill('https://api.agify.io');
	await page.getByLabel('Reason (required)').fill(REASON);
	const vendor = await posted(page, 'create', () =>
		page.getByRole('button', { name: 'Create vendor' }).click()
	);
	expect(vendor.get('auth_scheme')).toBe('none');
	await expect(page).toHaveURL('/vendors/agify');

	// 2. Endpoint: GET / with a required query parameter `name`, built from a pasted sample.
	await page.getByRole('link', { name: 'Add endpoint' }).click();
	await expect(page).toHaveURL('/vendors/agify/endpoints/new');
	await page.getByLabel('Name (required)').fill('estimate');
	await page.getByLabel('Method').selectOption('GET');
	await page.getByLabel('Path (required)').fill('/');
	const query = page.getByRole('group', { name: /^Query parameters/ });
	await query.getByRole('button', { name: 'Paste a sample' }).click();
	await query.getByLabel('Sample query parameters (JSON)').fill('{ "name": "Michael" }');
	await query.getByRole('button', { name: 'Generate fields' }).click();
	await expect(query.getByLabel('Type of name')).toHaveValue('string');
	await query.getByRole('checkbox', { name: 'name required' }).check();
	await page.getByLabel('Reason (required)').fill(REASON);
	await expectNoSeriousA11yViolations(page);
	const endpoint = await posted(page, 'createEndpoint', () =>
		page.getByRole('button', { name: 'Add endpoint' }).click()
	);
	expect(Object.fromEntries(endpoint)).toMatchObject({
		name: 'estimate',
		method: 'GET',
		path: '/'
	});
	expect([...endpoint.values()].join('\n')).toContain('"required":["name"]');
	await expect(page).toHaveURL('/vendors/agify/endpoints/estimate@1');
	await expect(page.getByText(/^Created endpoint estimate@1: GET \//)).toBeVisible();
	await expect(
		page.getByRole('region', { name: 'Query parameters of estimate@1' }).getByRole('row', {
			name: /name string Required/
		})
	).toBeVisible();

	// 3. Account: no credentials for an auth none vendor.
	await page.goto('/accounts/new?vendor_id=agify');
	await page.getByLabel('Account id (required)').fill('agify-public');
	await expect(page.getByLabel('Vendor (required)')).toHaveValue('agify');
	await page.getByLabel('Region (required)').selectOption('global');
	await expect(page.getByRole('note')).toContainText('this account has no credentials');
	await expect(page.locator('input[type="password"]')).toHaveCount(0);
	await page.getByLabel('Reason (required)').fill(REASON);
	const account = await posted(page, 'create', () =>
		page.getByRole('button', { name: 'Create account' }).click()
	);
	expect(Object.fromEntries(account)).toMatchObject({
		account_id: 'agify-public',
		vendor_id: 'agify',
		region: 'global'
	});
	await expect(page).toHaveURL('/accounts/agify-public');

	// 4. Request transformer: name = firstName + lastName, proven by a sample.
	await page.goto('/transformers/new');
	await page.getByLabel('Transformer id (required)').fill('AGE_REQ');
	await page.getByLabel('Kind').selectOption('request');
	await page.getByLabel('Vendor endpoint').fill('agify.estimate@1');
	await page
		.getByLabel('Vendor documentation the mapping was checked against')
		.fill('https://agify.io/documentation');
	await writeSource(
		page,
		'transformer: AGE_REQ\nkind: request\nvendor_endpoint: agify.estimate@1\nrequest:\n  method: GET\n  path: /\n  query:\n    name: req.firstName + req.lastName\n'
	);
	const reqSample = page.getByRole('listitem', { name: /^Sample 1/ });
	await reqSample.getByLabel('Name', { exact: true }).fill('full_name');
	await reqSample.getByLabel('Input body').fill('{ "firstName": "Mich", "lastName": "ael" }');
	await reqSample
		.getByLabel('Expected vendor request')
		.fill('{ "method": "GET", "path": "/", "query": { "name": "Michael" } }');
	await proveAndCreate(page, 'AGE_REQ@1');

	// 5. Response transformer: keep only `age`.
	await page.goto('/transformers/new');
	await page.getByLabel('Transformer id (required)').fill('AGE_RES');
	await page.getByLabel('Kind').selectOption('response');
	await expect(page.getByLabel('Vendor endpoint')).toHaveCount(0);
	await page
		.getByLabel('Vendor documentation the mapping was checked against')
		.fill('https://agify.io/documentation');
	await writeSource(page, 'transformer: AGE_RES\nkind: response\nresponse:\n  age: res.body.age\n');
	const resSample = page.getByRole('listitem', { name: /^Sample 1/ });
	await resSample.getByLabel('Name', { exact: true }).fill('known_name');
	await resSample
		.getByLabel('Vendor response')
		.fill(
			'{ "status": 200, "headers": { "Content-Type": "application/json" }, "body": { "count": 21, "name": "Michael", "age": 57 } }'
		);
	await resSample.getByLabel('Expected output').fill('{ "age": 57 }');
	await proveAndCreate(page, 'AGE_RES@1');

	// 6. Integration: not interactive (fixed at creation), then its first draft.
	await page.goto('/integrations/new');
	await page.getByLabel('Integration id (required)').fill('int_age_estimate');
	await page.getByLabel('Owner tenant (required)').selectOption('orders-service');
	await page.getByLabel('Description').fill('Estimate the age for a first and last name.');
	await expect(page.getByRole('checkbox', { name: /^Interactive/ })).not.toBeChecked();
	await page.getByLabel('Reason (required)').fill(REASON);
	const integration = await posted(page, 'create', () =>
		page.getByRole('button', { name: 'Create integration' }).click()
	);
	expect(integration.get('integration_id')).toBe('int_age_estimate');
	await expect(page).toHaveURL('/integrations/int_age_estimate/publish');

	// Operation `default`: body {firstName, lastName} from a pasted sample, both required.
	const op = page.getByRole('listitem', { name: 'Operation default' });
	await op
		.getByLabel('Description (shown to callers)')
		.fill('Estimate the age for a first and last name.');
	await op.getByRole('checkbox', { name: /^Idempotent/ }).check();
	await expect(op.getByRole('switch', { name: /Check live calls/ })).toBeChecked();
	const body = op.getByRole('group', { name: 'Request body (required)' });
	await body.getByRole('button', { name: 'Paste a sample' }).click();
	await body
		.getByLabel('Sample request body (JSON)')
		.fill('{ "firstName": "Mich", "lastName": "ael" }');
	await body.getByRole('button', { name: 'Generate fields' }).click();
	// Generated fields start as required (the operator unticks optional ones).
	await expect(body.getByRole('checkbox', { name: 'firstName required' })).toBeChecked();
	await expect(body.getByRole('checkbox', { name: 'lastName required' })).toBeChecked();
	await expect(body.getByLabel('Type of firstName')).toHaveValue('string');

	// Target: agify, the public account, AGE_REQ@1 and AGE_RES@1.
	await page.getByRole('button', { name: 'Add target' }).click();
	await page.getByLabel('Vendor', { exact: true }).fill('agify');
	await page.getByLabel('Account', { exact: true }).fill('agify-public');
	await page.getByLabel('Weight (%)').fill('100');
	await page.getByLabel('Request transformer', { exact: true }).fill('AGE_REQ@1');
	await page.getByLabel('Response transformer (optional)', { exact: true }).fill('AGE_RES@1');
	await page.getByLabel('Mode').selectOption('sync');
	await page.getByLabel('Why this version (required)').fill(REASON);
	await expectNoSeriousA11yViolations(page);
	const draft = await posted(page, 'saveDraft', () =>
		page.getByRole('button', { name: 'Create draft' }).click()
	);
	const spec = draft.get('spec') ?? '';
	expect(spec).toMatch(/required:\s*\n\s*- firstName\s*\n\s*- lastName/);
	expect(spec).toContain('validate_input: true');
	expect(spec).toContain('request: AGE_REQ@1');
	expect(spec).toContain('response: AGE_RES@1');
	await expect(page.getByText('Saved draft v1. All publish checks pass.')).toBeVisible();

	// 7. Publish, with a reason in the confirm dialog.
	await expect(page.getByText('All 11 checks pass. The draft can be published.')).toBeVisible();
	await page.getByRole('button', { name: 'Publish v1' }).click();
	const dialog = page.getByRole('dialog', { name: 'Publish int_age_estimate v1' });
	await expect(dialog.getByRole('button', { name: 'Publish v1' })).toBeDisabled();
	await dialog.getByLabel('Reason (required)').fill(REASON);
	const publish = await posted(page, 'publish', () =>
		dialog.getByRole('button', { name: 'Publish v1' }).click()
	);
	expect(Object.fromEntries(publish)).toMatchObject({ version: '1', expected_active_version: '' });
	await expect(page.getByText(/^Published v1\. It is now the active version/)).toBeVisible();
});

/** Replaces the transformer source in the code editor. */
async function writeSource(page: Page, yaml: string) {
	const source = page.getByLabel('Source (transformer YAML)');
	await source.fill(yaml);
}

/** Runs the samples on the draft (Create unlocks only then) and creates the version. */
async function proveAndCreate(page: Page, ref: string) {
	const create = page.getByRole('button', { name: `Create ${ref}` });
	await expect(create).toBeDisabled();
	await page.getByRole('button', { name: 'Run samples on this draft' }).click();
	await expect(page.getByText('All samples pass on this draft.')).toBeVisible();
	await expect(create).toBeEnabled();
	await create.click();
	const dialog = page.getByRole('dialog', { name: `Create ${ref}` });
	await dialog.getByLabel('Reason (required)').fill(REASON);
	const sent = await posted(page, 'createVersion', () =>
		dialog.getByRole('button', { name: `Create ${ref}` }).click()
	);
	expect(sent.get('fixtures')).toBeTruthy();
	const [id, version] = ref.split('@');
	await expect(page).toHaveURL(`/transformers/${id}/versions/${version}`);
}

test('a failing sample says which field broke and keeps Create locked', async ({ page }) => {
	await useScenario(page, 'samples-fail+onboarding');
	await signIn(page, { role: 'editor', next: '/transformers/new' });
	await page.getByLabel('Transformer id (required)').fill('AGE_REQ');
	await page.getByLabel('Vendor endpoint').fill('agify.estimate@1');
	await writeSource(
		page,
		'transformer: AGE_REQ\nkind: request\nvendor_endpoint: agify.estimate@1\nrequest:\n  method: GET\n  path: /\n  query:\n    name: req.firstName + req.lastName\n'
	);
	const first = page.getByRole('listitem', { name: /^Sample 1/ });
	await first.getByLabel('Name', { exact: true }).fill('full_name');
	await first.getByLabel('Input body').fill('{ "firstName": "Mich", "lastName": "ael" }');
	await first
		.getByLabel('Expected vendor request')
		.fill('{ "method": "GET", "path": "/", "query": { "name": "Michael" } }');
	await page.getByRole('button', { name: 'Add sample' }).click();
	const second = page.getByRole('listitem', { name: /^Sample 2/ });
	await second.getByLabel('Name', { exact: true }).fill('empty_last_name');
	await second.getByLabel('Input body').fill('{ "firstName": "", "lastName": "" }');
	await second
		.getByLabel('Expected vendor request')
		.fill('{ "method": "GET", "path": "/", "query": { "name": "Michael" } }');

	await page.getByRole('button', { name: 'Run samples on this draft' }).click();
	const why = page.getByRole('group', { name: 'Why sample empty_last_name fails' });
	await expect(why).toContainText('/query/name');
	await expect(why).toContainText('must NOT have fewer than 1 characters');
	await expect(page.getByRole('group', { name: 'Why sample full_name fails' })).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Create AGE_REQ@1' })).toBeDisabled();
	await expect(page.getByText(/1 of 2 samples fail/).first()).toBeVisible();
	await expectNoSeriousA11yViolations(page);
});

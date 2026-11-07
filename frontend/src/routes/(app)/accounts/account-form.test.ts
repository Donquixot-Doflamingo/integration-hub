import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import {
	createBody,
	credentialProblem,
	credentialsFromForm,
	editValuesFromAccount,
	emptyValues,
	mergePatch,
	problems,
	rotationStep,
	secretValues,
	updateBody,
	type FormValues
} from './account-form';
import { parseListState, searchFor } from './filters';

const NOW = Date.parse('2026-11-07T00:00:00Z');
const filled = (over: Partial<FormValues> = {}): FormValues => ({
	...emptyValues(),
	account_id: 'ct-brandC-in',
	vendor_id: 'clevertap',
	region: 'in',
	config: '{"project_id": "P-1"}',
	credentials: [{ key: 'passcode', value: 'example-passcode-not-real' }],
	...over
});

describe('account create form', () => {
	it.each<[string, Partial<FormValues>, string, RegExp]>([
		['id', { account_id: 'a' }, '/account_id', /3 to 64/],
		['vendor', { vendor_id: '' }, '/vendor_id', /Pick the vendor/],
		['region', { region: '' }, '/region', /regions/],
		['config not json', { config: '{' }, '/config', /not valid JSON/],
		['config not an object', { config: '[]' }, '/config', /JSON object/],
		['rps', { rps: 'fast' }, '/quota/rps', /requests per second/],
		['daily', { daily: '1.5' }, '/quota/daily', /whole number/],
		['no credentials', { credentials: [{ key: '', value: '' }] }, '/credentials', /at least one/],
		[
			'expiry past',
			{ credential_expires_at: '2026-01-01T00:00' },
			'/credential_expires_at',
			/future/
		]
	])('%s', (_n, over, ptr, msg) => {
		expect(problems(filled(over), NOW)[ptr]).toMatch(msg);
	});

	it.each([
		[[{ key: 'passcode', value: '' }], /Enter the value of "passcode"/],
		[[{ key: '', value: 'x' }], /needs a key/],
		[[{ key: 'pass code', value: 'x' }], /letters, digits/],
		[
			[
				{ key: 'a', value: 'x' },
				{ key: 'a', value: 'y' }
			],
			/listed twice/
		]
	])('credential rows %j', (rows, msg) => {
		expect(credentialProblem(rows)).toMatch(msg);
	});

	it('builds AccountCreate with credentials, config object and UTC expiry', () => {
		const r = createBody(
			filled({ rps: '50', daily: '5000000', credential_expires_at: '2027-10-31T00:00' }),
			'Brand C India workspace',
			NOW
		);
		expect(r).toEqual({
			ok: true,
			body: {
				account_id: 'ct-brandC-in',
				vendor_id: 'clevertap',
				region: 'in',
				config: { project_id: 'P-1' },
				quota: { rps: 50, daily: 5000000 },
				credentials: { passcode: 'example-passcode-not-real' },
				credential_expires_at: '2027-10-31T00:00:00Z',
				reason: 'Brand C India workspace'
			}
		});
	});

	it('pairs posted credential keys and values; lists the secret values to scrub', () => {
		const form = new FormData();
		form.append('cred_key', 'account_id');
		form.append('cred_value', 'A-1');
		form.append('cred_key', 'passcode');
		form.append('cred_value', 's3cr3t');
		const rows = credentialsFromForm(form);
		expect(rows).toEqual([
			{ key: 'account_id', value: 'A-1' },
			{ key: 'passcode', value: 's3cr3t' }
		]);
		expect(secretValues(rows)).toEqual(['A-1', 's3cr3t']);
	});
});

describe('config and quota edit (merge-patch)', () => {
	it('patches changed keys, nulls removed ones, recurses into objects', () => {
		expect(
			mergePatch(
				{ project_id: 'P-1', brand_code: 'A', nested: { a: 1, b: 2 }, same: [1] },
				{ project_id: 'P-2', nested: { a: 1, c: 3 }, same: [1], added: true }
			)
		).toEqual({ project_id: 'P-2', brand_code: null, nested: { b: null, c: 3 }, added: true });
	});

	it('sends only what changed and refuses an empty patch', () => {
		const before = editValuesFromAccount(contractMocks.getAccount.example);
		expect(before.rps).toBe('50');
		expect(updateBody(before, before, 'nothing changed here').ok).toBe(false);
		const r = updateBody({ ...before, rps: '80' }, before, 'Vendor raised our quota');
		expect(r).toEqual({
			ok: true,
			body: { quota: { rps: 80 }, reason: 'Vendor raised our quota' }
		});
	});
});

describe('rotation step', () => {
	const s = (slot: 'active' | 'next' | 'previous', v: number) => ({
		slot,
		cred_version: v,
		fingerprint: 'sha256:x',
		created_at: '2026-11-01T00:00:00Z'
	});
	it.each([
		[[s('active', 3)], null, 'add'],
		[[s('active', 3), s('next', 4)], null, 'test'],
		[[s('active', 3), s('next', 4)], 3, 'test'],
		[[s('active', 3), s('next', 4)], 4, 'promote'],
		[[s('active', 4), s('previous', 3)], 4, 'delete']
	] as const)('%j tested=%s -> %s', (slots, tested, want) => {
		expect(rotationStep(slots, tested)).toBe(want);
	});
});

describe('accounts list filters', () => {
	it('reads vendor, state and search from the URL and ignores bad values', () => {
		const s = parseListState(
			new URLSearchParams('vendor_id=clevertap&state=draining&q=brand&limit=25')
		);
		expect(s.query).toEqual({ limit: 25, vendor_id: 'clevertap', state: 'draining', q: 'brand' });
		expect(parseListState(new URLSearchParams('state=gone&vendor_id=a b')).filters).toEqual({});
		expect(searchFor({ vendor_id: 'clevertap', state: 'active' })).toBe(
			'?vendor_id=clevertap&state=active'
		);
	});
});

import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import {
	createKeyBody,
	isLive,
	KEY_ID,
	keyName,
	keyProblems,
	keyValuesFromForm,
	overlapFromForm,
	overlapNote,
	SCOPE,
	type KeyFormValues
} from './keys';

// HUB-271: API key create / rotate input against ApiKeyCreate / ApiKeyRotate.

const NOW = Date.parse('2026-10-31T10:00:00Z');
const base: KeyFormValues = { scopes: ['invoke'], integration: '', description: '', expires: '' };

describe('keyProblems', () => {
	it.each<{ name: string; v: Partial<KeyFormValues>; field: string | null }>([
		{ name: 'invoke only', v: {}, field: null },
		{ name: 'no scope', v: { scopes: [] }, field: 'scopes' },
		{ name: 'unknown scope', v: { scopes: ['invoke', 'admin'] }, field: 'scopes' },
		{ name: 'bad integration', v: { integration: 'a b' }, field: 'integration' },
		{ name: 'integration ok', v: { integration: 'int_evt_brandA' }, field: null },
		{ name: 'expiry in the past', v: { expires: '2026-10-30T10:00' }, field: 'expires_at' },
		{ name: 'expiry now', v: { expires: '2026-10-31T10:00' }, field: 'expires_at' },
		{ name: 'not a time', v: { expires: 'tomorrow' }, field: 'expires_at' },
		{ name: 'future expiry', v: { expires: '2027-01-01T00:00' }, field: null }
	])('$name', ({ v, field }) => {
		const out = keyProblems({ ...base, ...v }, NOW);
		if (field) expect(Object.keys(out)).toEqual([field]);
		else expect(out).toEqual({});
	});
});

describe('createKeyBody', () => {
	it('builds ApiKeyCreate in contract scope order, narrowed to one integration', () => {
		const r = createKeyBody(
			'orders-service',
			{
				scopes: ['requests_read', 'invoke'],
				integration: ' int_evt_brandA ',
				description: ' orders prod ',
				expires: '2027-01-01T00:00'
			},
			'Initial production key',
			NOW
		);
		expect(r).toEqual({
			ok: true,
			body: {
				tenant_id: 'orders-service',
				description: 'orders prod',
				scopes: ['invoke:int_evt_brandA', 'requests_read:int_evt_brandA'],
				expires_at: '2027-01-01T00:00:00Z',
				reason: 'Initial production key'
			}
		});
		if (r.ok) for (const s of r.body.scopes) expect(s).toMatch(SCOPE);
	});

	it('matches the contract request example for a plain invoke key', () => {
		// hub-admin.v1.yaml, createApiKey requestBody example.
		expect(
			createKeyBody(
				'orders-service',
				{ ...base, description: 'orders prod' },
				'Initial production key',
				NOW
			)
		).toEqual({
			ok: true,
			body: {
				tenant_id: 'orders-service',
				description: 'orders prod',
				scopes: ['invoke'],
				expires_at: null,
				reason: 'Initial production key'
			}
		});
	});
});

it('reads the posted scopes', () => {
	const f = new FormData();
	f.append('scope', 'invoke');
	f.append('scope', 'dry_run');
	f.set('integration', 'x');
	expect(keyValuesFromForm(f)).toEqual({
		scopes: ['invoke', 'dry_run'],
		integration: 'x',
		description: '',
		expires: ''
	});
});

it.each([
	['86400000', 86_400_000],
	['0', 0],
	['2592000000', 2_592_000_000],
	['2592000001', null],
	['-1', null],
	['', null]
])('overlapFromForm(%s)', (raw, want) => {
	const f = new FormData();
	f.set('overlap_ms', raw);
	expect(overlapFromForm(f)).toBe(want);
});

describe('overlapNote', () => {
	const old = contractMocks.rotateApiKey.example.old_key;
	it('says until when the old key works', () => {
		expect(overlapNote(old, 86_400_000)).toBe(
			'The old key hk_live_7Qm2 keeps working until 2026-11-01 10:00 UTC (overlap 24 h), then it is revoked automatically. Switch callers to the new key before then.'
		);
	});
	it('says it stopped without an overlap', () => {
		expect(overlapNote(old, 0)).toMatch(/no longer works/);
	});
});

it('key ids and live states', () => {
	expect(KEY_ID.test(contractMocks.getApiKey.example.key_id)).toBe(true);
	expect(KEY_ID.test('key_1')).toBe(false);
	const k = contractMocks.getApiKey.example;
	expect(isLive(k)).toBe(true);
	expect(isLive({ ...k, state: 'expiring' })).toBe(true);
	expect(isLive({ ...k, state: 'revoked' })).toBe(false);
	expect(isLive({ ...k, state: 'expired' })).toBe(false);
});

// HUB-289: keys seeded from the config tree have no prefix (the Hub answers "").
describe('keyName', () => {
	it.each([
		['hk_live_7Qm2', 'key_01J9Z7F6G7H8J9K0M1N2P3Q4RS', 'hk_live_7Qm2'],
		['', 'key_01J9Z7F6G7H8J9K0M1N2P3Q4RS', 'key_01J9Z7F6G7H8J9K0M1N2P3Q4RS'],
		['   ', 'key_01J9Z7F6G7H8J9K0M1N2P3Q4RS', 'key_01J9Z7F6G7H8J9K0M1N2P3Q4RS']
	])('prefix %j -> %s', (prefix, key_id, want) => {
		expect(keyName({ prefix, key_id })).toBe(want);
	});
});

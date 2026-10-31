import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import {
	createBody,
	EMPTY_VALUES,
	hasChanges,
	problems,
	rateLimitText,
	updateBody,
	valuesFromForm,
	valuesFromTenant,
	type FieldValues
} from './tenant-form';

// HUB-270: tenant create / edit checked against TenantCreate / TenantUpdate.

const valid: FieldValues = {
	...EMPTY_VALUES,
	tenant_id: 'orders-service',
	display_name: 'Orders service'
};
const tenant = contractMocks.getTenant.example;

describe('problems', () => {
	it.each<{ name: string; v: Partial<FieldValues>; field: keyof FieldValues | null }>([
		{ name: 'valid', v: {}, field: null },
		{ name: 'missing id', v: { tenant_id: '' }, field: 'tenant_id' },
		{ name: 'id too short', v: { tenant_id: 'ab' }, field: 'tenant_id' },
		{ name: 'id with space', v: { tenant_id: 'orders service' }, field: 'tenant_id' },
		{ name: 'id starting with -', v: { tenant_id: '-orders' }, field: 'tenant_id' },
		{ name: 'id of 65 chars', v: { tenant_id: 'a'.repeat(65) }, field: 'tenant_id' },
		{ name: 'blank name', v: { display_name: '   ' }, field: 'display_name' },
		{ name: 'name of 129 chars', v: { display_name: 'n'.repeat(129) }, field: 'display_name' },
		{ name: 'negative rps', v: { rps: '-1' }, field: 'rps' },
		{ name: 'rps not a number', v: { rps: 'fast' }, field: 'rps' },
		{ name: 'decimal rps', v: { rps: '12.5' }, field: null },
		{ name: 'decimal burst', v: { burst: '1.5' }, field: 'burst' },
		{ name: 'zero burst', v: { burst: '0' }, field: null }
	])('create: $name', ({ v, field }) => {
		const out = problems({ ...valid, ...v }, 'create');
		if (field) expect(Object.keys(out)).toEqual([field]);
		else expect(out).toEqual({});
	});

	it('edit does not check the id (it never changes)', () => {
		expect(problems({ ...valid, tenant_id: '' }, 'edit')).toEqual({});
	});
});

describe('createBody', () => {
	it('trims, maps empty optional fields to null and sends the rate limit as numbers', () => {
		const r = createBody(
			{ ...valid, tenant_id: ' orders-service ', contact: '  ', rps: '200', burst: '400' },
			'Pilot tenant for orders'
		);
		expect(r).toEqual({
			ok: true,
			body: {
				tenant_id: 'orders-service',
				display_name: 'Orders service',
				description: null,
				contact: null,
				rate_limit: { rps: 200, burst: 400 },
				reason: 'Pilot tenant for orders'
			}
		});
	});

	it('omits the rate limit when both fields are empty', () => {
		const r = createBody(valid, 'Pilot tenant for orders');
		expect(r.ok && 'rate_limit' in r.body).toBe(false);
	});

	it('returns the field problems instead of a body', () => {
		expect(createBody({ ...valid, tenant_id: 'x' }, 'r')).toMatchObject({
			ok: false,
			fields: { tenant_id: expect.any(String) }
		});
	});
});

describe('updateBody (merge-patch)', () => {
	const before = valuesFromTenant(tenant);

	it('reads the contract example into form values', () => {
		expect(before).toEqual({
			tenant_id: 'orders-service',
			display_name: 'Orders service',
			description: 'Order lifecycle events and notifications',
			contact: 'orders-team@example.com',
			rps: '200',
			burst: '400'
		});
	});

	it('sends only what changed', () => {
		expect(
			updateBody({ ...before, rps: '300', burst: '600' }, before, 'Festive season traffic')
		).toEqual({
			ok: true,
			body: { rate_limit: { rps: 300, burst: 600 }, reason: 'Festive season traffic' }
		});
		expect(updateBody({ ...before, rps: '300' }, before, 'Festive season traffic')).toEqual({
			ok: true,
			body: { rate_limit: { rps: 300 }, reason: 'Festive season traffic' }
		});
	});

	it('clears a nullable field with null', () => {
		expect(updateBody({ ...before, contact: '' }, before, 'Team moved')).toEqual({
			ok: true,
			body: { contact: null, reason: 'Team moved' }
		});
	});

	it('keeps a rate limit value when its field is emptied (merge-patch cannot remove it)', () => {
		expect(hasChanges({ ...before, rps: '' }, before)).toBe(false);
	});

	it('treats 200 and 200.0 as the same rate', () => {
		expect(hasChanges({ ...before, rps: '200.0' }, before)).toBe(false);
	});

	it('refuses an edit that changes nothing', () => {
		expect(updateBody(before, before, 'Nothing to do')).toMatchObject({
			ok: false,
			fields: { display_name: 'Nothing changed. Edit at least one field.' }
		});
	});
});

it('reads posted values and the hidden before-values', () => {
	const f = new FormData();
	f.set('display_name', 'New');
	f.set('before.display_name', 'Old');
	expect(valuesFromForm(f).display_name).toBe('New');
	expect(valuesFromForm(f, 'before.').display_name).toBe('Old');
	expect(valuesFromForm(f).rps).toBe('');
});

it.each([
	[{ rps: 200, burst: 400 }, '200 requests/s, burst 400'],
	[{ rps: 12.5 }, '12.5 requests/s'],
	[{}, 'No limit set']
])('rateLimitText(%o)', (rl, want) => {
	expect(rateLimitText(rl)).toBe(want);
});

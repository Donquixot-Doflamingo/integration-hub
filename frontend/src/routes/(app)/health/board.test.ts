import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import type { Breaker } from '$lib/api/types';
import {
	buildBoard,
	deadLettersOf,
	forcedLabel,
	retryLabel,
	summarizeBoard,
	totalDeadLetters
} from './board';

// HUB-261: health board grouping and ordering (worst first).

const base = contractMocks.listBreakers.example.items[0]!;
const b = (over: Partial<Breaker>): Breaker => ({ ...base, ...over });

describe('buildBoard', () => {
	const breakers = [
		b({ vendor_id: 'moengage', account_id: 'mo-a', state: 'closed', parked_count: 0 }),
		b({ vendor_id: 'clevertap', account_id: 'ct-b', state: 'half_open', parked_count: 5 }),
		b({ vendor_id: 'moengage', account_id: 'mo-b', state: 'open', parked_count: 10 }),
		b({ vendor_id: 'clevertap', account_id: 'ct-a', state: 'open', parked_count: 1290 }),
		b({ vendor_id: 'clevertap', account_id: 'ct-c', state: 'closed', parked_count: 0 })
	];
	const accounts = [
		{
			...contractMocks.listAccounts.example.items[0]!,
			account_id: 'ct-a',
			state: 'draining' as const
		}
	];

	it('groups by vendor, worst group first, worst rows first', () => {
		const groups = buildBoard(breakers, accounts);
		expect(groups.map((g) => g.vendorId)).toEqual(['clevertap', 'moengage']);
		expect(groups[0]!.rows.map((r) => r.breaker.account_id)).toEqual(['ct-a', 'ct-b', 'ct-c']);
		expect(groups[1]!.rows.map((r) => r.breaker.account_id)).toEqual(['mo-b', 'mo-a']);
	});

	it('joins the account lifecycle state, null when unknown', () => {
		const groups = buildBoard(breakers, accounts);
		expect(groups[0]!.rows[0]!.lifecycle).toBe('draining');
		expect(groups[0]!.rows[1]!.lifecycle).toBeNull();
	});

	it('breaks ties on parked count, then oldest parked', () => {
		const groups = buildBoard(
			[
				b({ account_id: 'x', parked_count: 1, parked_oldest_age_ms: 10 }),
				b({ account_id: 'y', parked_count: 1, parked_oldest_age_ms: 900 })
			],
			[]
		);
		expect(groups[0]!.rows.map((r) => r.breaker.account_id)).toEqual(['y', 'x']);
	});

	it('summarizes states, forced and parked', () => {
		expect(
			summarizeBoard([...breakers, b({ state: 'closed', forced: 'closed', parked_count: 0 })])
		).toEqual({
			open: 2,
			halfOpen: 1,
			closed: 3,
			forced: 1,
			parked: 1305
		});
	});
});

describe('retryLabel', () => {
	const now = Date.parse('2026-10-31T09:40:37Z');
	it.each([
		[{ state: 'open', open_until: '2026-10-31T09:41:00Z' }, 'retry in 23 s'],
		[{ state: 'open', open_until: '2026-10-31T09:50:00Z' }, 'retry in 10 min'],
		[{ state: 'open', open_until: '2026-10-31T09:40:00Z' }, 'probe due'],
		[{ state: 'open', open_until: null }, null],
		[{ state: 'closed', open_until: '2026-10-31T09:41:00Z' }, null],
		[{ state: 'open', open_until: '2026-10-31T09:41:00Z', forced: 'open' }, null]
	] as const)('%j -> %s', (x, want) => expect(retryLabel(x, now)).toBe(want));
});

describe('forcedLabel', () => {
	it.each([
		[{ forced: 'none', forced_until: null }, null],
		[{ forced: 'open', forced_until: '2026-10-31T10:30:00Z' }, 'Forced open until 10:30 UTC'],
		[{ forced: 'closed', forced_until: null }, 'Forced closed until cleared']
	] as const)('%j -> %s', (x, want) => expect(forcedLabel(x)).toBe(want));
});

describe('dead letters per account (HUB-284)', () => {
	const account = contractMocks.listAccounts.example.items[0]!;

	it('reads the account figure, null when the API sent none', () => {
		expect(deadLettersOf(account)).toEqual({
			count: account.dead_letter_count,
			oldestAt: account.dead_letter_oldest_at
		});
		expect(
			deadLettersOf({ ...account, dead_letter_count: 0, dead_letter_oldest_at: null })
		).toEqual({ count: 0, oldestAt: null });
		const { dead_letter_count: _c, dead_letter_oldest_at: _o, ...without } = account;
		expect(deadLettersOf(without)).toBeNull();
		expect(deadLettersOf(undefined)).toBeNull();
	});

	it('joins it onto every endpoint row of the account', () => {
		const groups = buildBoard(
			[b({ account_id: 'a', endpoint_ref: 'x@1' }), b({ account_id: 'a', endpoint_ref: 'y@1' })],
			[{ ...account, account_id: 'a', dead_letter_count: 7 }]
		);
		expect(groups[0]!.rows.map((r) => r.deadLetters?.count)).toEqual([7, 7]);
	});

	it('totals each account once, null when no account has a figure', () => {
		const accounts = [
			{ ...account, account_id: 'a', dead_letter_count: 7 },
			{ ...account, account_id: 'b', dead_letter_count: 5 },
			{ ...account, account_id: 'c', dead_letter_count: 100 }
		];
		const breakers = [
			b({ account_id: 'a', endpoint_ref: 'x@1' }),
			b({ account_id: 'a', endpoint_ref: 'y@1' }),
			b({ account_id: 'b' })
		];
		expect(totalDeadLetters(breakers, accounts)).toBe(12);
		expect(totalDeadLetters(breakers, [])).toBeNull();
	});
});

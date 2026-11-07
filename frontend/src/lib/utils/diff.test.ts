import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import {
	foldUnchanged,
	formatValue,
	pointerToPath,
	splitUnified,
	summarize,
	type SplitRow
} from './diff';

const example = contractMocks.diffIntegrationVersions.example;

describe('splitUnified', () => {
	it('splits the contract example into side-by-side rows', () => {
		const d = splitUnified(example.unified);
		expect(d.oldTitle).toBe('v7');
		expect(d.newTitle).toBe('v8');
		expect(d.rows).toEqual([
			{ kind: 'hunk', text: 'targets' },
			{
				kind: 'line',
				left: { text: '  weight: 100', type: 'removed' },
				right: { text: '  weight: 90', type: 'added' }
			},
			{ kind: 'line', left: null, right: { text: '- vendor_id: clevertap', type: 'added' } },
			{ kind: 'line', left: null, right: { text: '  account_id: ct-brandA-in-v2', type: 'added' } }
		]);
	});

	it('keeps context on both sides and pairs uneven runs', () => {
		const d = splitUnified('--- a\n+++ b\n@@ x @@\n keep\n-one\n-two\n+uno\n keep2\n');
		expect(d.rows).toEqual([
			{ kind: 'hunk', text: 'x' },
			{
				kind: 'line',
				left: { text: 'keep', type: 'context' },
				right: { text: 'keep', type: 'context' }
			},
			{
				kind: 'line',
				left: { text: 'one', type: 'removed' },
				right: { text: 'uno', type: 'added' }
			},
			{ kind: 'line', left: { text: 'two', type: 'removed' }, right: null },
			{
				kind: 'line',
				left: { text: 'keep2', type: 'context' },
				right: { text: 'keep2', type: 'context' }
			}
		]);
	});

	it('handles an empty diff', () => {
		expect(splitUnified('')).toEqual({ oldTitle: null, newTitle: null, rows: [] });
	});
});

describe('foldUnchanged', () => {
	const ctx = (n: number): SplitRow[] =>
		Array.from({ length: n }, (_, i) => ({
			kind: 'line' as const,
			left: { text: `c${i}`, type: 'context' as const },
			right: { text: `c${i}`, type: 'context' as const }
		}));

	it('folds long unchanged runs and keeps three lines around changes', () => {
		const change: SplitRow = { kind: 'line', left: { text: 'x', type: 'removed' }, right: null };
		const out = foldUnchanged([...ctx(20), change]);
		expect(out).toHaveLength(3 + 1 + 3 + 1);
		expect(out[3]).toMatchObject({ kind: 'fold', id: 0 });
		expect((out[3] as { rows: SplitRow[] }).rows).toHaveLength(14);
	});

	it('leaves short runs alone', () => {
		expect(foldUnchanged(ctx(10))).toHaveLength(10);
	});
});

describe('pointerToPath', () => {
	it.each([
		['/targets/0/weight', 'targets[0].weight'],
		['/targets/1', 'targets[1]'],
		['/delivery/wait_ms', 'delivery.wait_ms'],
		['/a~1b/c~0d', 'a/b.c~d'],
		['', '(whole spec)']
	])('%s -> %s', (p, want) => expect(pointerToPath(p)).toBe(want));
});

describe('summarize', () => {
	it('counts and lists the changed paths', () => {
		expect(summarize(example.changes)).toBe(
			'3 paths differ (2 added, 1 changed): targets[0].weight, targets[1], operations.default.request_schema.body.properties.context.properties.channel'
		);
		expect(summarize([])).toBe('No differences.');
	});
});

describe('formatValue', () => {
	it.each([
		[100, '100'],
		['T1@8', 'T1@8'],
		[null, 'null'],
		[undefined, ''],
		[{ a: 1 }, '{\n  "a": 1\n}']
	])('%j', (v, want) => expect(formatValue(v)).toBe(want));
});

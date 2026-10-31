import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { jsonDiff } from './json-diff';

// HUB-274: before / after of a change log entry as DiffOps.

describe('jsonDiff', () => {
	it.each<{ name: string; before: unknown; after: unknown; want: unknown[] }>([
		{
			name: 'the contract example (publish)',
			before: contractMocks.getChangeLogEntry.example.before,
			after: contractMocks.getChangeLogEntry.example.after,
			want: [{ op: 'replace', path: '/active_version', before: 7, after: 8 }]
		},
		{ name: 'equal', before: { a: 1, b: [1, 2] }, after: { a: 1, b: [1, 2] }, want: [] },
		{
			name: 'nested change, added and removed keys',
			before: { rate_limit: { rps: 200, burst: 400 }, contact: 'x' },
			after: { rate_limit: { rps: 300, burst: 400 }, disabled: true },
			want: [
				{ op: 'replace', path: '/rate_limit/rps', before: 200, after: 300 },
				{ op: 'remove', path: '/contact', before: 'x' },
				{ op: 'add', path: '/disabled', after: true }
			]
		},
		{
			name: 'arrays index by index',
			before: { grants: ['a', 'b'] },
			after: { grants: ['a', 'c', 'd'] },
			want: [
				{ op: 'replace', path: '/grants/1', before: 'b', after: 'c' },
				{ op: 'add', path: '/grants/2', after: 'd' }
			]
		},
		{
			name: 'create (before null)',
			before: null,
			after: { tenant_id: 't', disabled: false },
			want: [
				{ op: 'add', path: '/tenant_id', after: 't' },
				{ op: 'add', path: '/disabled', after: false }
			]
		},
		{
			name: 'delete (after null)',
			before: { endpoint_id: 'reg' },
			after: null,
			want: [{ op: 'remove', path: '/endpoint_id', before: 'reg' }]
		},
		{
			name: 'type change is a replace',
			before: { v: { x: 1 } },
			after: { v: [1] },
			want: [{ op: 'replace', path: '/v', before: { x: 1 }, after: [1] }]
		},
		{
			name: 'pointer escaping',
			before: { 'a/b': 1, 'c~d': 1 },
			after: { 'a/b': 2, 'c~d': 2 },
			want: [
				{ op: 'replace', path: '/a~1b', before: 1, after: 2 },
				{ op: 'replace', path: '/c~0d', before: 1, after: 2 }
			]
		},
		{ name: 'both null', before: null, after: null, want: [] }
	])('$name', ({ before, after, want }) => {
		expect(jsonDiff(before, after)).toEqual(want);
	});
});

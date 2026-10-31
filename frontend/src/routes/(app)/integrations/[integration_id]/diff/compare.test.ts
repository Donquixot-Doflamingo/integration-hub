import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import type { IntegrationVersionSummary } from '$lib/api/types';
import { compareSearch, defaultPair, parseCompare, versionOption } from './compare';

const [v8, v7] = contractMocks.listIntegrationVersions.example.items as [
	IntegrationVersionSummary,
	IntegrationVersionSummary
];
const v = (version: number, extra: Partial<IntegrationVersionSummary> = {}) => ({
	...v7,
	version,
	is_active: false,
	based_on: version - 1,
	...extra
});

describe('parseCompare', () => {
	it.each([
		{ s: 'from=7&to=8', want: { from: 7, to: 8, view: 'split' } },
		{ s: 'from=7&to=8&view=paths', want: { from: 7, to: 8, view: 'paths' } },
		{ s: 'from=0&to=x&view=nope', want: { from: null, to: null, view: 'split' } },
		{ s: 'from=1.5', want: { from: null, to: null, view: 'split' } }
	])('$s', ({ s, want }) => expect(parseCompare(new URLSearchParams(s))).toEqual(want));
});

describe('defaultPair', () => {
	it('compares the newest version with the active one (contract example: v7 -> v8)', () => {
		expect(defaultPair([v8, v7])).toEqual({ from: 7, to: 8 });
	});
	it('falls back to based_on, then to the previous version', () => {
		expect(defaultPair([v(9, { is_active: true, based_on: 5 }), v(5), v(8)])).toEqual({
			from: 5,
			to: 9
		});
		expect(defaultPair([v(3, { based_on: null }), v(2)])).toEqual({ from: 2, to: 3 });
	});
	it('needs two versions', () => {
		expect(defaultPair([v7])).toBeNull();
	});
});

describe('labels and links', () => {
	it('names versions and builds compare queries', () => {
		expect(versionOption(v8)).toBe('v8 (draft)');
		expect(versionOption(v7)).toBe('v7 (active)');
		expect(versionOption(v(6))).toBe('v6');
		expect(compareSearch(7, 8)).toBe('?from=7&to=8');
		expect(compareSearch(7, 8, 'paths')).toBe('?from=7&to=8&view=paths');
	});
});

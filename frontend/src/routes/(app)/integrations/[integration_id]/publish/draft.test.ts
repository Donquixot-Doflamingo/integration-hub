import { describe, expect, it } from 'vitest';
import { checkLabel } from './draft';

// HUB-289: every check the contract knows has a label; unknown ones read as words, not ids.
describe('checkLabel', () => {
	it.each([
		['weights_sum_100', 'Weights total 100'],
		['operations_have_transformers', 'Every operation has a transformer'],
		['brand_new_check', 'Brand new check'],
		['', '']
	])('%j -> %j', (check, want) => expect(checkLabel(check)).toBe(want));

	it('labels every check name listed in the contract', async () => {
		const { readFileSync } = await import('node:fs');
		const { join } = await import('node:path');
		const yaml = readFileSync(
			join(process.cwd(), '..', 'contracts', 'openapi', 'hub-admin.v1.yaml'),
			'utf8'
		);
		const known = /Known: ([a-z0-9_, ]+?)(?: \(|\. )/.exec(yaml)?.[1] ?? '';
		const names = known.split(/,\s*/).filter(Boolean);
		expect(names.length).toBeGreaterThan(10);
		for (const n of names) expect([n, checkLabel(n)]).not.toEqual([n, n]);
	});
});

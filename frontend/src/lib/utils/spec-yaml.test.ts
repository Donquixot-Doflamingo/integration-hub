import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { parseSpecYaml, pointerLine, pointerTokens, specToYaml } from './spec-yaml';

// HUB-259: the spec editor's YAML round trip and pointer -> line mapping.

const spec = contractMocks.createIntegrationDraft.example.integration_version.spec;

describe('specToYaml / parseSpecYaml', () => {
	it('round-trips a contract spec', () => {
		const text = specToYaml(spec);
		expect(text).toMatch(/^operations:\n {2}default:\n/);
		expect(text).toMatch(/\ntargets:\n {2}- vendor_id: clevertap/);
		const parsed = parseSpecYaml(text);
		expect(parsed).toEqual({ ok: true, value: spec });
	});

	it('accepts JSON', () => {
		expect(parseSpecYaml(JSON.stringify(spec))).toEqual({ ok: true, value: spec });
	});

	it.each([
		['', 'The spec is empty.', null],
		['targets: [1,\ndelivery: x', /indented|flow/i, 2],
		['- a\n- b', 'The spec must be a mapping (targets, delivery, audit, ...).', 1],
		['audit: 1\naudit: 2', /unique|duplicate/i, 2]
	])('reports %j with its line', (text, message, line) => {
		const r = parseSpecYaml(text);
		expect(r.ok).toBe(false);
		if (r.ok) return;
		expect(r.message).toMatch(message);
		expect(r.line).toBe(line);
	});
});

describe('pointerTokens', () => {
	it.each([
		['', []],
		['/', []],
		['/targets/1/account_id', ['targets', 1, 'account_id']],
		['/a~1b/c~0d', ['a/b', 'c~d']]
	])('%s', (p, want) => expect(pointerTokens(p)).toEqual(want));
});

describe('pointerLine', () => {
	const text = specToYaml(spec);
	const lineOf = (needle: string, nth = 0) =>
		text
			.split('\n')
			.findIndex(
				(l, i, all) =>
					l.includes(needle) && all.slice(0, i).filter((x) => x.includes(needle)).length === nth
			) + 1;

	it.each([
		['/targets', lineOf('targets:')],
		['/targets/1', lineOf('account_id: ct-brandA-in-v2') - 1],
		['/targets/1/account_id', lineOf('account_id: ct-brandA-in-v2')],
		['/targets/0/weight', lineOf('weight: 90')],
		['/delivery/mode', lineOf('mode: wait')],
		// Missing property: reported at the nearest parent that exists.
		['/delivery/retry/max_attempts', lineOf('delivery:')],
		['/nope', null]
	])('%s -> line %s', (pointer, line) => expect(pointerLine(text, pointer)).toBe(line));

	it('works on hand-edited flow style too', () => {
		const flow = 'targets: [ { vendor_id: a, weight: 90 },\n  { vendor_id: b, weight: 10 } ]\n';
		expect(pointerLine(flow, '/targets/1/weight')).toBe(2);
	});

	it('is null when the text does not parse', () => {
		expect(pointerLine('a: [', '/a')).toBeNull();
	});
});

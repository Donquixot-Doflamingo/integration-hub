import { describe, expect, it } from 'vitest';
import { weightsFromForm, weightsProblem } from './weights';

// HUB-260: weights must total 100.

describe('weightsProblem', () => {
	it.each([
		[[50, 50], [90, 10], null],
		[[100], [], null],
		[[50, 60], [90, 10], 'Weights must total 100 (now 110).'],
		[[0, 0], [], 'Weights must total 100 (now 0).'],
		[[NaN, 100], [], 'Each weight is a whole number from 0 to 100.'],
		[[101, -1], [], 'Each weight is a whole number from 0 to 100.'],
		[[90, 10], [90, 10], 'Change at least one weight; these are the current weights.'],
		[[], [], 'The active version has no targets.']
	])('%j (current %j) -> %j', (values, current, want) =>
		expect(weightsProblem(values, current)).toBe(want)
	);
});

describe('weightsFromForm', () => {
	const form = (fields: Record<string, string>) => {
		const f = new FormData();
		for (const [k, v] of Object.entries(fields)) f.set(k, v);
		return f;
	};
	const two = {
		targets: '2',
		vendor_0: 'clevertap',
		account_0: 'a',
		vendor_1: 'clevertap',
		account_1: 'b'
	};

	it('reads every target in order', () => {
		expect(weightsFromForm(form({ ...two, weight_0: '70', weight_1: '30' }))).toEqual({
			ok: true,
			weights: [
				{ vendor_id: 'clevertap', account_id: 'a', weight: 70 },
				{ vendor_id: 'clevertap', account_id: 'b', weight: 30 }
			]
		});
	});

	it.each([
		[{ ...two, weight_0: '70', weight_1: '40' }, 'Weights must total 100 (now 110).'],
		[{ ...two, weight_0: '7.5', weight_1: '92.5' }, 'Each weight is a whole number from 0 to 100.'],
		[{ ...two, weight_0: '', weight_1: '100' }, 'Each weight is a whole number from 0 to 100.'],
		[{ targets: '1', weight_0: '100' }, 'Reload the page: a target is missing.'],
		[{ weight_0: '100' }, 'Reload the page: the targets are not known.']
	])('%j -> %s', (fields, message) =>
		expect(weightsFromForm(form(fields as Record<string, string>))).toEqual({ ok: false, message })
	);
});

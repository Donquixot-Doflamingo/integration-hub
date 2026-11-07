import { describe, expect, it } from 'vitest';
import { claimingPointer, detailsToPointerFields } from './actions';

describe('detailsToPointerFields (HUB-290)', () => {
	const fields = ['/regions', '/defaults/breaker', '/defaults/breaker/window_ms', '/reason'];

	it.each([
		['/regions/in', '/regions'],
		['/regions', '/regions'],
		['/defaults/breaker/window_ms', '/defaults/breaker/window_ms'],
		['/defaults/breaker/min_calls', '/defaults/breaker'],
		['/regionsx', null],
		['/display_name', null],
		[undefined, null]
	])('%s is claimed by %s', (pointer, want) => {
		expect(claimingPointer(pointer, fields)).toBe(want);
	});

	it('puts the first detail per field on it and keeps the rest for the summary', () => {
		const { fields: out, rest } = detailsToPointerFields(
			[
				{ pointer: '/regions/in', message: 'must be a URI' },
				{ pointer: '/regions/us', message: 'second one' },
				{ pointer: '/docs_url', message: 'bad url' },
				{ message: 'no pointer' }
			],
			fields
		);
		expect(out).toEqual({ '/regions': 'must be a URI' });
		expect(rest.map((d) => d.message)).toEqual(['second one', 'bad url', 'no pointer']);
	});
});

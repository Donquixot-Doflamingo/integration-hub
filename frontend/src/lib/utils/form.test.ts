import { describe, expect, it } from 'vitest';
import { blockersFrom } from './form';

describe('blockersFrom', () => {
	it('labels pointer keys as paths and keeps plain keys', () => {
		expect(
			blockersFrom({ '/defaults/breaker/window_ms': 'too small', name: 'empty', '/x': undefined })
		).toEqual([
			{ label: 'defaults.breaker.window_ms', message: 'too small' },
			{ label: 'name', message: 'empty' }
		]);
	});
});

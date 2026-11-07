import { describe, expect, it } from 'vitest';
import { setFlash, takeFlash } from './flash.svelte';

describe('flash notice', () => {
	it('is handed over once, then gone', () => {
		expect(takeFlash()).toBeNull();
		setFlash({ message: 'Created vendor acme.', changeId: 'chg_1', requestId: null });
		expect(takeFlash()).toEqual({
			message: 'Created vendor acme.',
			changeId: 'chg_1',
			requestId: null
		});
		expect(takeFlash()).toBeNull();
	});
});

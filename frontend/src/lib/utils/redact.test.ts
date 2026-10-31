import { describe, expect, it } from 'vitest';
import { REDACTED, redactSecrets } from './redact';

// HUB-264: payload views never show secret-looking values the API failed to mask.

describe('redactSecrets', () => {
	it('hides unmasked secret fields at any depth and keeps everything else', () => {
		const input = {
			headers: { Authorization: 'Bearer abc.def', 'X-Api-Key': 'k-123', accept: 'json' },
			body: { user: { id: 'u1', password: 'hunter2' }, items: [{ token: 't' }, { name: 'x' }] }
		};
		expect(redactSecrets(input)).toEqual({
			headers: { Authorization: REDACTED, 'X-Api-Key': REDACTED, accept: 'json' },
			body: { user: { id: 'u1', password: REDACTED }, items: [{ token: REDACTED }, { name: 'x' }] }
		});
		// The input is not modified.
		expect(input.headers.Authorization).toBe('Bearer abc.def');
	});

	it.each(['sha256:1c9e', 'hmac:7f1a', '***', '[redacted]', '<redacted>'])(
		'keeps values the API already masked: %s',
		(v) => expect(redactSecrets({ authorization: v })).toEqual({ authorization: v })
	);

	it('leaves empty and null secrets alone, and non-objects as they are', () => {
		expect(redactSecrets({ token: null, secret: '' })).toEqual({ token: null, secret: '' });
		expect(redactSecrets('plain')).toBe('plain');
		expect(redactSecrets([1, null])).toEqual([1, null]);
	});
});

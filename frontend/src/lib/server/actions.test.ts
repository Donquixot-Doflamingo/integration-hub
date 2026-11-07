import { describe, expect, it } from 'vitest';
import type { PlainApiError } from '$lib/api/actions';
import { finishByPointer, scrubSecrets } from './actions';

const error = (over: Partial<PlainApiError> = {}): PlainApiError => ({
	status: 400,
	code: 'VALIDATION_FAILED',
	message: 'credentials.passcode s3cr3t-value is too short',
	retryable: false,
	retryAfterMs: null,
	requestId: 'req_01J9Z3M1A5B7C9D1E3F5G7H9KC',
	details: [{ pointer: '/credentials/passcode', message: 'value s3cr3t-value rejected' }],
	meta: { echo: 's3cr3t-value' },
	...over
});

describe('scrubSecrets (HUB-291)', () => {
	it('replaces every occurrence of a submitted secret in message, details and meta', () => {
		const out = scrubSecrets(error(), ['s3cr3t-value']);
		expect(JSON.stringify(out)).not.toContain('s3cr3t-value');
		expect(out.message).toBe('credentials.passcode [redacted] is too short');
		expect(out.details[0]!.message).toBe('value [redacted] rejected');
		expect(out.meta).toEqual({ echo: '[redacted]' });
		expect(out.requestId).toBe('req_01J9Z3M1A5B7C9D1E3F5G7H9KC');
	});

	it('leaves the error alone without secrets (empty values are ignored)', () => {
		const e = error();
		expect(scrubSecrets(e, [''])).toBe(e);
	});
});

describe('finishByPointer (HUB-290)', () => {
	it('maps API pointers to fields and scrubs secrets from the failure', () => {
		const res = finishByPointer(
			'create',
			{ ok: false, status: 400, requestId: null, error: { ...error(), envelope: null } as never },
			['/credentials'],
			['s3cr3t-value']
		);
		expect('status' in res && res.status).toBe(400);
		const data = (res as { data: { fields: Record<string, string>; error: PlainApiError } }).data;
		expect(data.fields).toEqual({ '/credentials': 'value [redacted] rejected' });
		expect(JSON.stringify(data)).not.toContain('s3cr3t-value');
	});
});

import { describe, expect, it } from 'vitest';
import { loginUrl, safeNext } from './redirect';

// HUB-253: `next` after login is untrusted input; only same-origin paths survive.
describe('safeNext', () => {
	it.each([
		{
			next: '/integrations?tenant_id=orders-service',
			want: '/integrations?tenant_id=orders-service'
		},
		{ next: '/dlq#top', want: '/dlq#top' },
		{ next: '/', want: '/' },
		{ next: null, want: '/' },
		{ next: '', want: '/' },
		{ next: 'integrations', want: '/' },
		{ next: '//evil.example/x', want: '/' },
		{ next: '/\\evil.example', want: '/' },
		{ next: 'https://evil.example', want: '/' },
		{ next: 'javascript:alert(1)', want: '/' },
		{ next: '/x\r\nSet-Cookie: a=b', want: '/' },
		{ next: `/${'a'.repeat(3000)}`, want: '/' }
	])('$next -> $want', ({ next, want }) => {
		expect(safeNext(next)).toBe(want);
	});
});

describe('loginUrl', () => {
	it('carries the requested page and the expired flag', () => {
		expect(loginUrl(new URL('http://c.test/dlq?tenant_id=a'))).toBe(
			'/login?next=%2Fdlq%3Ftenant_id%3Da'
		);
		expect(loginUrl(new URL('http://c.test/'))).toBe('/login');
		expect(loginUrl(new URL('http://c.test/'), { expired: true })).toBe('/login?expired=1');
	});
});

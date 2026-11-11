import { describe, expect, it } from 'vitest';
import { open, seal } from './crypto';
import { decodeSession, encodeSession, newSession } from './session';

const SECRET = 'k'.repeat(40);

// HUB-492: the session cookie is AES-GCM encrypted; the token is not readable from its value.
describe('seal / open', () => {
	it('round-trips', () => {
		const v = seal({ a: 1, b: ['x'] }, SECRET, 'session');
		expect(open(v, SECRET, 'session')).toEqual({ a: 1, b: ['x'] });
	});

	it('does not reveal the payload, and is different every time (random IV)', () => {
		const a = seal({ token: 'super-secret-token' }, SECRET, 'session');
		const b = seal({ token: 'super-secret-token' }, SECRET, 'session');
		expect(a).not.toBe(b);
		for (const v of [a, b]) {
			expect(v).not.toContain('super-secret-token');
			expect(Buffer.from(v, 'base64url').toString('latin1')).not.toContain('super-secret-token');
			expect(Buffer.from(v, 'base64').toString('latin1')).not.toContain('super-secret-token');
		}
	});

	it.each([
		['wrong secret', (v: string) => open(v, 'z'.repeat(40), 'session')],
		['another purpose', (v: string) => open(v, SECRET, 'login')],
		[
			'a flipped bit in the ciphertext',
			(v: string) => {
				const raw = Buffer.from(v, 'base64url');
				raw[raw.length - 1]! ^= 1;
				return open(raw.toString('base64url'), SECRET, 'session');
			}
		],
		[
			'a flipped bit in the tag',
			(v: string) => {
				const raw = Buffer.from(v, 'base64url');
				raw[14]! ^= 1;
				return open(raw.toString('base64url'), SECRET, 'session');
			}
		],
		['a truncated value', (v: string) => open(v.slice(0, 20), SECRET, 'session')],
		['garbage', () => open('!!!not base64!!!', SECRET, 'session')],
		['nothing', () => open(undefined, SECRET, 'session')]
	])('refuses %s', (_name, attempt) => {
		const v = seal({ ok: true }, SECRET, 'session');
		expect(attempt(v)).toBeNull();
	});
});

describe('session cookie value', () => {
	const session = newSession({
		provider: 'isfc',
		user: { subject: 'u1', name: 'P', email: 'p@example.com', roles: [], tenants: [] },
		accessToken: 'eyJhbGciOi.access-token-payload.sig',
		refreshToken: 'refresh-token-value',
		isfcSessionId: 'sess-77',
		accessExp: 4102444800,
		deviceId: 'device-00000000-0000-4000-8000-000000000000'
	});

	it('does not contain the access or refresh token in any readable form', () => {
		const value = encodeSession(session, SECRET);
		for (const secret of [
			'access-token-payload',
			'refresh-token-value',
			'sess-77',
			'p@example.com'
		]) {
			expect(value).not.toContain(secret);
			expect(Buffer.from(value, 'base64url').toString('utf8')).not.toContain(secret);
			expect(Buffer.from(value, 'base64url').toString('latin1')).not.toContain(secret);
		}
	});

	it('round-trips on the server, which is the only place that can read it', () => {
		expect(decodeSession(encodeSession(session, SECRET), SECRET)).toEqual(session);
	});

	it('a login-state value cannot be used as a session, and the reverse', () => {
		const login = seal({ v: 1 }, SECRET, 'login');
		expect(decodeSession(login, SECRET)).toBeNull();
		expect(open(encodeSession(session, SECRET), SECRET, 'login')).toBeNull();
	});
});

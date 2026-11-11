import { describe, expect, it, vi } from 'vitest';
import {
	clearSessionCookie,
	readSessionCookie,
	sessionSetCookieHeaders,
	writeSessionCookie
} from './cookie';
import { decodeSession, newSession } from './session';

const SECRET = 'c'.repeat(40);
const session = (extra = '') =>
	newSession({
		provider: 'isfc',
		user: { subject: 'u', name: null, email: null, roles: [], tenants: [] },
		accessToken: `access.${extra}.sig`,
		refreshToken: 'refresh',
		isfcSessionId: 's',
		accessExp: 4102444800
	});

function jar() {
	const store = new Map<string, string>();
	return {
		store,
		get: (k: string) => store.get(k),
		set: vi.fn((k: string, v: string, _opts?: unknown) => void store.set(k, v)),
		delete: vi.fn((k: string) => void store.delete(k)),
		serialize: (k: string, v: string, o: { maxAge?: number; secure?: boolean }) =>
			`${k}=${v}; Max-Age=${o.maxAge}; Secure=${o.secure}`
	};
}

describe('session cookie chunks', () => {
	it('a normal session fits one cookie', () => {
		const j = jar();
		writeSessionCookie(j as never, session(), SECRET, true);
		expect([...j.store.keys()]).toEqual(['hub_console_session']);
		expect(j.set.mock.calls[0]![2]).toMatchObject({
			httpOnly: true,
			sameSite: 'lax',
			secure: true,
			path: '/'
		});
		expect(decodeSession(readSessionCookie(j as never), SECRET)).toEqual(
			expect.objectContaining({ provider: 'isfc' })
		);
	});

	it('a large token is split under 4 KB per cookie and joined on read', () => {
		const j = jar();
		const big = session('x'.repeat(6000));
		writeSessionCookie(j as never, big, SECRET, false);
		expect(j.store.size).toBeGreaterThan(1);
		for (const [k, v] of j.store) expect(`${k}=${v}`.length).toBeLessThan(3700);
		expect(decodeSession(readSessionCookie(j as never), SECRET)).toEqual(big);
	});

	it('a shorter session removes the stale tail of the longer one', () => {
		const j = jar();
		writeSessionCookie(j as never, session('x'.repeat(6000)), SECRET, false);
		writeSessionCookie(j as never, session(), SECRET, false);
		expect([...j.store.keys()]).toEqual(['hub_console_session']);
	});

	it('refuses a session that cannot fit', () => {
		expect(() =>
			writeSessionCookie(jar() as never, session('x'.repeat(40000)), SECRET, false)
		).toThrow(/too large/);
	});

	it('clear deletes every chunk, with the Secure flag of the page', () => {
		const j = jar();
		clearSessionCookie(j as never, false);
		expect(j.delete).toHaveBeenCalledTimes(6);
		expect(j.delete.mock.calls[0]).toEqual(['hub_console_session', { path: '/', secure: false }]);
		const k = jar();
		clearSessionCookie(k as never);
		expect(k.delete.mock.calls[0]).toEqual(['hub_console_session', { path: '/' }]);
	});
});

describe('sessionSetCookieHeaders (for responses SvelteKit does not add cookies to)', () => {
	it('serializes the session chunks and blanks the unused ones', () => {
		const headers = sessionSetCookieHeaders(jar() as never, session(), SECRET, true);
		expect(headers).toHaveLength(6);
		expect(headers[0]).toMatch(
			/^hub_console_session=[A-Za-z0-9_-]{50,}; Max-Age=\d+; Secure=true$/
		);
		expect(headers[1]).toBe('hub_console_session.1=; Max-Age=0; Secure=true');
	});

	it('an ended session deletes everything', () => {
		const headers = sessionSetCookieHeaders(jar() as never, null, SECRET, false);
		expect(headers).toHaveLength(6);
		expect(headers.every((h) => h.includes('Max-Age=0'))).toBe(true);
	});
});

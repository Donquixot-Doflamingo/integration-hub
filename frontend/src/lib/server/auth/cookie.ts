import type { Cookies } from '@sveltejs/kit';
import { SESSION_COOKIE, SESSION_TTL_SECONDS, encodeSession, type Session } from './session';

/** The part of SvelteKit's `Cookies` the session code uses (tests pass a plain jar). */
export type CookieJar = Pick<Cookies, 'get' | 'set' | 'delete'>;

/**
 * Browsers drop a cookie over 4096 bytes without a word. A session with an ISFC access and
 * refresh token can come close once encrypted, so the value is split over `hub_console_session`,
 * `hub_console_session.1`, ... and joined on read. Short sessions use the first one only.
 */
const CHUNK = 3500;
const MAX_CHUNKS = 6;
const name = (i: number) => (i === 0 ? SESSION_COOKIE : `${SESSION_COOKIE}.${i}`);

/** The sealed session value from the cookie (all chunks joined), or undefined. */
export function readSessionCookie(cookies: Pick<CookieJar, 'get'>): string | undefined {
	let value = cookies.get(name(0));
	if (!value) return undefined;
	for (let i = 1; i < MAX_CHUNKS; i++) {
		const part = cookies.get(name(i));
		if (!part) break;
		value += part;
	}
	return value;
}

/**
 * Forgets the session cookie, every chunk. `secure` should say whether the page is https: a
 * delete that is `Secure` while the cookie was not (SvelteKit's default for any host but
 * localhost) is ignored by browsers on plain http.
 */
export function clearSessionCookie(cookies: Pick<CookieJar, 'delete'>, secure?: boolean): void {
	for (let i = 0; i < MAX_CHUNKS; i++) {
		cookies.delete(name(i), secure === undefined ? { path: '/' } : { path: '/', secure });
	}
}

interface CookiePart {
	name: string;
	value: string;
	opts: { path: string; httpOnly: boolean; sameSite: 'lax'; secure: boolean; maxAge: number };
}

/** The cookies (value chunks) that hold `session`. */
function sessionCookieParts(session: Session, secret: string, secure: boolean): CookiePart[] {
	const value = encodeSession(session, secret);
	const parts = value.match(new RegExp(`.{1,${CHUNK}}`, 'g')) ?? [value];
	if (parts.length > MAX_CHUNKS) throw new Error('The session is too large for a cookie.');
	const maxAge = Math.min(
		Math.max(0, session.exp - Math.floor(Date.now() / 1000)),
		SESSION_TTL_SECONDS
	);
	return parts.map((part, i) => ({
		name: name(i),
		value: part,
		opts: { path: '/', httpOnly: true, sameSite: 'lax', secure, maxAge }
	}));
}

/** Seals `session` and sets the cookie: httpOnly, SameSite=Lax, Secure on https. */
export function writeSessionCookie(
	cookies: CookieJar,
	session: Session,
	secret: string,
	secure: boolean
): void {
	const parts = sessionCookieParts(session, secret, secure);
	for (const p of parts) cookies.set(p.name, p.value, p.opts);
	// A shorter value than before must not leave a stale tail behind.
	for (let i = parts.length; i < MAX_CHUNKS; i++) {
		if (cookies.get(name(i))) cookies.delete(name(i), { path: '/' });
	}
}

/**
 * `Set-Cookie` header values for the session as it is now (`null`: the session ended, every
 * chunk is deleted). SvelteKit adds cookies set with `cookies.set` only to responses that come
 * out of `resolve()`; the admin proxy answers with its own `Response`, so it must carry them
 * itself or a refreshed (rotated) token would never reach the browser.
 */
export function sessionSetCookieHeaders(
	cookies: Pick<Cookies, 'serialize'>,
	session: Session | null,
	secret: string | null,
	secure: boolean
): string[] {
	const out: string[] = [];
	const parts = session && secret ? sessionCookieParts(session, secret, secure) : [];
	for (const p of parts) out.push(cookies.serialize(p.name, p.value, p.opts));
	for (let i = parts.length; i < MAX_CHUNKS; i++) {
		out.push(cookies.serialize(name(i), '', { path: '/', maxAge: 0, secure }));
	}
	return out;
}

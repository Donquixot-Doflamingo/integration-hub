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

/** Forgets the session cookie, every chunk. */
export function clearSessionCookie(cookies: Pick<CookieJar, 'delete'>): void {
	for (let i = 0; i < MAX_CHUNKS; i++) cookies.delete(name(i), { path: '/' });
}

/** Seals `session` and sets the cookie: httpOnly, SameSite=Lax, Secure on https. */
export function writeSessionCookie(
	cookies: CookieJar,
	session: Session,
	secret: string,
	secure: boolean
): void {
	const value = encodeSession(session, secret);
	const parts = value.match(new RegExp(`.{1,${CHUNK}}`, 'g')) ?? [value];
	if (parts.length > MAX_CHUNKS) throw new Error('The session is too large for a cookie.');
	const maxAge = Math.max(0, session.exp - Math.floor(Date.now() / 1000));
	parts.forEach((part, i) =>
		cookies.set(name(i), part, {
			path: '/',
			httpOnly: true,
			sameSite: 'lax',
			secure,
			maxAge: Math.min(maxAge, SESSION_TTL_SECONDS)
		})
	);
	// A shorter value than before must not leave a stale tail behind.
	for (let i = parts.length; i < MAX_CHUNKS; i++) {
		if (cookies.get(name(i))) cookies.delete(name(i), { path: '/' });
	}
}

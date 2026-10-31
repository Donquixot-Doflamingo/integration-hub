import { createHmac, timingSafeEqual } from 'node:crypto';
import type { SessionUser } from '$lib/api/types';
import { isRole } from '$lib/auth/roles';

/**
 * The console session: one httpOnly, SameSite=Lax cookie, HMAC-SHA256 signed, set only by server
 * code (the login action). Never readable from JavaScript, never in localStorage. It carries the
 * identity and, for OIDC (HUB-282), the access token, which stays on the server: pages get
 * `PublicSession` only.
 */
export const SESSION_COOKIE = 'hub_console_session';
/** Session lifetime: one on-call shift. */
export const SESSION_TTL_SECONDS = 8 * 60 * 60;

export type AuthProviderId = 'dev' | 'oidc';

export interface Session {
	v: 1;
	provider: AuthProviderId;
	user: SessionUser;
	/** Bearer token for `/admin/v1`; server side only. Null for the dev provider. */
	accessToken: string | null;
	/** Expiry, epoch seconds. */
	exp: number;
}

/** What the browser may know about the session. */
export interface PublicSession {
	provider: AuthProviderId;
	user: SessionUser;
	expiresAt: string;
}

export function toPublicSession(s: Session): PublicSession {
	return { provider: s.provider, user: s.user, expiresAt: new Date(s.exp * 1000).toISOString() };
}

function sign(payload: string, secret: string): string {
	return createHmac('sha256', secret).update(payload).digest('base64url');
}

export function newSession(
	init: Pick<Session, 'provider' | 'user' | 'accessToken'>,
	now = Date.now()
): Session {
	return { v: 1, ...init, exp: Math.floor(now / 1000) + SESSION_TTL_SECONDS };
}

/** `<base64url(json)>.<base64url(hmac)>` */
export function encodeSession(session: Session, secret: string): string {
	const payload = Buffer.from(JSON.stringify(session)).toString('base64url');
	return `${payload}.${sign(payload, secret)}`;
}

function isSession(x: unknown): x is Session {
	if (typeof x !== 'object' || x === null) return false;
	const s = x as Record<string, unknown>;
	const u = s.user as Record<string, unknown> | undefined;
	return (
		s.v === 1 &&
		(s.provider === 'dev' || s.provider === 'oidc') &&
		typeof s.exp === 'number' &&
		(s.accessToken === null || typeof s.accessToken === 'string') &&
		typeof u === 'object' &&
		u !== null &&
		typeof u.subject === 'string' &&
		Array.isArray(u.roles) &&
		u.roles.every(isRole) &&
		Array.isArray(u.tenants) &&
		u.tenants.every((t) => typeof t === 'string')
	);
}

/** The session in a cookie value, or null when missing, tampered, malformed or expired. */
export function decodeSession(
	value: string | undefined,
	secret: string,
	now = Date.now()
): Session | null {
	if (!value) return null;
	const dot = value.indexOf('.');
	if (dot <= 0) return null;
	const payload = value.slice(0, dot);
	const given = Buffer.from(value.slice(dot + 1));
	const want = Buffer.from(sign(payload, secret));
	if (given.length !== want.length || !timingSafeEqual(given, want)) return null;
	let parsed: unknown;
	try {
		parsed = JSON.parse(Buffer.from(payload, 'base64url').toString('utf8'));
	} catch {
		return null;
	}
	if (!isSession(parsed) || parsed.exp * 1000 <= now) return null;
	return parsed;
}

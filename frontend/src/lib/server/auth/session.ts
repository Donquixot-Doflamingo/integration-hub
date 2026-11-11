import type { SessionUser } from '$lib/api/types';
import { isRole } from '$lib/auth/roles';
import { open, seal } from './crypto';

/**
 * The console session: one httpOnly, SameSite=Lax cookie, AES-256-GCM encrypted (HUB-492), set
 * only by server code. Never readable from JavaScript, never in localStorage, and the value does
 * not reveal the tokens inside it. It carries the identity and, for ISFC, the access and refresh
 * tokens, which stay on the server: pages get `PublicSession` only (ADR-0005, ADR-0022).
 */
export const SESSION_COOKIE = 'hub_console_session';
/** Session lifetime: one on-call shift. */
export const SESSION_TTL_SECONDS = 8 * 60 * 60;

export type AuthProviderId = 'dev' | 'isfc';

export interface Session {
	v: 2;
	provider: AuthProviderId;
	user: SessionUser;
	/** Bearer token for `/admin/v1`; server side only. Null for the dev provider in mock mode. */
	accessToken: string | null;
	/** ISFC refresh token; server side only. */
	refreshToken: string | null;
	/** ISFC session id (needed to refresh and to log out). */
	isfcSessionId: string | null;
	/** Access token expiry, epoch seconds; null when there is no ISFC token. */
	accessExp: number | null;
	/** ISFC tenant UUID the operator works in (sent as `X-Tenant-Id`); null: the gateway's default. */
	tenantId: string | null;
	/** The browser's stable device id, sent as `x-device-id` on ISFC calls. */
	deviceId: string | null;
	/** Expiry of the whole console session, epoch seconds. */
	exp: number;
}

/** What the browser may know about the session. */
export interface PublicSession {
	provider: AuthProviderId;
	user: SessionUser;
	expiresAt: string;
	/** The ISFC tenant the operator works in; null when unset or not ISFC. */
	tenantId: string | null;
}

export function toPublicSession(s: Session): PublicSession {
	return {
		provider: s.provider,
		user: s.user,
		expiresAt: new Date(s.exp * 1000).toISOString(),
		tenantId: s.tenantId
	};
}

export type NewSessionInit = Pick<Session, 'provider' | 'user' | 'accessToken'> &
	Partial<Pick<Session, 'refreshToken' | 'isfcSessionId' | 'accessExp' | 'tenantId' | 'deviceId'>>;

export function newSession(init: NewSessionInit, now = Date.now()): Session {
	return {
		v: 2,
		refreshToken: null,
		isfcSessionId: null,
		accessExp: null,
		tenantId: null,
		deviceId: null,
		...init,
		exp: Math.floor(now / 1000) + SESSION_TTL_SECONDS
	};
}

/** Sealed (AES-GCM) cookie value of the session. */
export function encodeSession(session: Session, secret: string): string {
	return seal(session, secret, 'session');
}

const nullableString = (x: unknown) => x === null || typeof x === 'string';

function isSession(x: unknown): x is Session {
	if (typeof x !== 'object' || x === null) return false;
	const s = x as Record<string, unknown>;
	const u = s.user as Record<string, unknown> | undefined;
	return (
		s.v === 2 &&
		(s.provider === 'dev' || s.provider === 'isfc') &&
		typeof s.exp === 'number' &&
		nullableString(s.accessToken) &&
		nullableString(s.refreshToken) &&
		nullableString(s.isfcSessionId) &&
		nullableString(s.tenantId) &&
		nullableString(s.deviceId) &&
		(s.accessExp === null || typeof s.accessExp === 'number') &&
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
	const parsed = open(value, secret, 'session');
	if (!isSession(parsed) || parsed.exp * 1000 <= now) return null;
	return parsed;
}

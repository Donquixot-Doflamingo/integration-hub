import type { RequestEvent } from '@sveltejs/kit';
import { readIsfcConfig, redirectUriFor, type IsfcConfig } from './config';
import { clearSessionCookie, writeSessionCookie } from './cookie';
import { deviceIdOf } from './device';
import { IsfcError, refreshTokens, type Fetch } from './isfc';
import type { Session } from './session';
import { sessionSecret } from './index';

/**
 * Keeping an ISFC session alive on the server (HUB-492, AUTH_PLAN §9): the access token is
 * refreshed shortly before it expires, or once after the gateway says it is no longer valid.
 * Refresh tokens rotate, so two refreshes with the same token must never run at once:
 * a refresh is single-flight per refresh token, and a late request that still carries the old
 * cookie within 30 s gets the same answer instead of a second call. A 409 from ISFC (another
 * node is rotating the same token) is retried once after 300 ms inside `refreshTokens`.
 */

/** Refresh when fewer than this many seconds are left on the access token. */
export const REFRESH_SKEW_SECONDS = 60;
/** How long a finished refresh is remembered for requests that still carry the old token. */
const REPLAY_MS = 30_000;

export type RefreshResult =
	| { ok: true; session: Session }
	/** ISFC refused the refresh token (revoked, expired, device mismatch): sign in again. */
	| { ok: false; ended: true }
	/** ISFC could not be reached (or answered 5xx): the session may still be fine. */
	| { ok: false; ended: false };

const inflight = new Map<string, Promise<RefreshResult>>();
const settled = new Map<string, { at: number; result: RefreshResult }>();

/** True when this session should be refreshed now. */
export function needsRefresh(session: Session, now = Date.now()): boolean {
	return (
		session.provider === 'isfc' &&
		!!session.refreshToken &&
		!!session.isfcSessionId &&
		session.accessExp !== null &&
		session.accessExp - now / 1000 < REFRESH_SKEW_SECONDS
	);
}

export interface RefreshDeps {
	config: IsfcConfig;
	fetch: Fetch;
	origin: string;
	userAgent: string | null;
	now?: number;
	sleep?: (ms: number) => Promise<void>;
}

async function run(session: Session, deps: RefreshDeps): Promise<RefreshResult> {
	try {
		const t = await refreshTokens(
			deps.config,
			deps.fetch,
			{
				refreshToken: session.refreshToken!,
				sessionId: session.isfcSessionId!,
				redirectUri: redirectUriFor(deps.config, deps.origin),
				deviceId: session.deviceId ?? '',
				userAgent: deps.userAgent
			},
			{ now: deps.now, sleep: deps.sleep }
		);
		return {
			ok: true,
			session: {
				...session,
				accessToken: t.accessToken,
				refreshToken: t.refreshToken ?? session.refreshToken,
				isfcSessionId: t.sessionId ?? session.isfcSessionId,
				accessExp: t.accessExp
			}
		};
	} catch (e) {
		const transient = !(e instanceof IsfcError) || e.transient;
		return transient ? { ok: false, ended: false } : { ok: false, ended: true };
	}
}

/** Refreshes `session`'s tokens, single-flight per refresh token. Never throws. */
export function refreshSession(session: Session, deps: RefreshDeps): Promise<RefreshResult> {
	const key = session.refreshToken;
	if (!key || !session.isfcSessionId) return Promise.resolve({ ok: false, ended: true });
	const now = deps.now ?? Date.now();
	for (const [k, v] of settled) if (now - v.at > REPLAY_MS) settled.delete(k);
	const recent = settled.get(key);
	// A transient failure is not replayed: the next request may try again.
	if (recent && (recent.result.ok || recent.result.ended)) return Promise.resolve(recent.result);
	const running = inflight.get(key);
	if (running) return running;
	const p = run(session, deps).then((result) => {
		inflight.delete(key);
		settled.set(key, { at: deps.now ?? Date.now(), result });
		return result;
	});
	inflight.set(key, p);
	return p;
}

/** Forgets remembered refreshes (tests). */
export function resetRefreshState(): void {
	inflight.clear();
	settled.clear();
}

export type RefreshOutcome = 'fresh' | 'refreshed' | 'ended' | 'failed';

type Ev = Pick<RequestEvent, 'cookies' | 'locals' | 'request' | 'url'>;

/**
 * Refreshes the request's session when it is due (or `force`d, after the gateway refused the
 * token), and writes the new cookie. `ended` clears the session and flags `locals.sessionEnded`,
 * so the login page says the session ended (`/login?expired=1`).
 */
export async function refreshForEvent(
	event: Ev,
	opts: { fetch?: Fetch; force?: boolean } = {}
): Promise<RefreshOutcome> {
	const session = event.locals.session;
	if (!session || session.provider !== 'isfc' || !session.refreshToken) return 'fresh';
	if (!opts.force && !needsRefresh(session)) return 'fresh';
	const cfg = readIsfcConfig();
	const secret = sessionSecret();
	if (!cfg.ok || !secret) return 'failed';
	const secure = event.url.protocol === 'https:';
	const withDevice: Session = {
		...session,
		deviceId: session.deviceId ?? deviceIdOf(event.cookies, secure)
	};
	const r = await refreshSession(withDevice, {
		config: cfg.config,
		fetch: opts.fetch ?? fetch,
		origin: event.url.origin,
		userAgent: event.request.headers.get('user-agent')
	});
	if (r.ok) {
		event.locals.session = r.session;
		event.locals.sessionDirty = true;
		writeSessionCookie(event.cookies, r.session, secret, secure);
		return 'refreshed';
	}
	if (r.ended) {
		clearSessionCookie(event.cookies, secure);
		event.locals.session = null;
		event.locals.sessionEnded = true;
		event.locals.sessionDirty = true;
		return 'ended';
	}
	return 'failed';
}

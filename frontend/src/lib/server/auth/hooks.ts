import type { Handle, HandleFetch } from '@sveltejs/kit';
import { adminApiBase } from '$lib/api/console';
import { resolveBaseUrl } from '$lib/api/client';
import { isTenantUuid } from '$lib/auth/tenant-choice';
import { clearSessionCookie, readSessionCookie } from './cookie';
import { decodeSession } from './session';
import { refreshForEvent } from './refresh';
import { sessionSecret } from './index';

/**
 * Reads the session cookie into `event.locals.session`, refreshing the ISFC tokens when due. A cookie that no longer verifies (wrong
 * key, tampered, expired) is dropped and `locals.sessionEnded` says so, so the login page can
 * tell the operator the session ended instead of showing a bare sign-in.
 */
export const authenticate: Handle = async ({ event, resolve }) => {
	const raw = readSessionCookie(event.cookies);
	const secret = sessionSecret();
	const session = raw && secret ? decodeSession(raw, secret) : null;
	if (raw && !session) clearSessionCookie(event.cookies);
	event.locals.session = session;
	event.locals.sessionEnded = !!raw && !session;
	// An ISFC access token about to expire is refreshed before anything uses it (single flight).
	// If ISFC refuses the refresh token the session is over: the page goes to /login?expired=1.
	await refreshForEvent(event, { fetch: event.fetch });
	return resolve(event);
};

/**
 * Server-side fetches to the admin API (SSR loads) carry the session's bearer token. The token
 * never reaches the browser; browser-side calls rely on the same-origin deployment (HUB-282).
 */
export const attachAdminToken: HandleFetch = async ({ event, request, fetch }) => {
	const token = event.locals.session?.accessToken;
	if (token && !request.headers.has('Authorization')) {
		const base = resolveBaseUrl(adminApiBase(), event.url.origin);
		if (request.url === base || request.url.startsWith(`${base}/`)) {
			request.headers.set('Authorization', `Bearer ${token}`);
			const tenant = event.locals.session?.tenantId;
			if (tenant && isTenantUuid(tenant) && !request.headers.has('X-Tenant-Id')) {
				request.headers.set('X-Tenant-Id', tenant);
			}
		}
	}
	return fetch(request);
};

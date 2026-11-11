import type { Handle, RequestEvent } from '@sveltejs/kit';
import { env } from '$env/dynamic/private';
import { DEFAULT_ADMIN_API_BASE, ETAG_HEADER, REQUEST_ID_HEADER } from '$lib/api/client';
import { isTokenRefusal, normalizeGatewayError } from './gateway-errors';
import { isTenantUuid } from '$lib/auth/tenant-choice';
import { sessionSetCookieHeaders, writeSessionCookie } from './auth/cookie';
import { refreshForEvent } from './auth/refresh';
import { sessionSecret } from './auth';

/**
 * Same-origin proxy for the admin API (HUB-286, ADR-0022).
 *
 * The console calls `/admin/v1/...` on its own origin, from SSR loads, form actions and client
 * navigations alike. When `HUB_ADMIN_UPSTREAM` (e.g. `http://127.0.0.1:8081`) is set, this hook
 * forwards those requests to the Hub admin server and adds the session's bearer token, so the
 * token never reaches browser JavaScript. Without it, `/admin/v1` must be routed to the admin
 * server by whatever sits in front of the console (the previous same-origin deployment model).
 *
 * Only the headers the admin contract uses are forwarded each way; nothing else leaks through.
 */
export const ADMIN_PREFIX = DEFAULT_ADMIN_API_BASE;

/** Request headers the admin API reads (contract: content type, ETag preconditions, tracing). */
const REQUEST_HEADERS = ['accept', 'content-type', 'if-match', 'if-none-match', 'traceparent'];
/** Response headers the console reads (contract `components.headers` and standard ones). */
const RESPONSE_HEADERS = [
	'content-type',
	'etag',
	'x-hub-request-id',
	'retry-after',
	'cache-control',
	'content-disposition',
	'location'
];
const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS']);

export function adminUpstream(): string | null {
	const v = env.HUB_ADMIN_UPSTREAM?.trim();
	return v ? v.replace(/\/+$/, '') : null;
}

/** True for requests the proxy owns: `/admin/v1` and everything below it. */
export function isAdminPath(pathname: string): boolean {
	return pathname === ADMIN_PREFIX || pathname.startsWith(`${ADMIN_PREFIX}/`);
}

/**
 * Cross-site request forgery guard for mutations: the session cookie is SameSite=Lax, and on top
 * a mutating call must come from this origin (browser `Origin` / `Sec-Fetch-Site`) or from the
 * console's own server code (form actions use `event.fetch`, a sub-request).
 */
export function sameOrigin(event: Pick<RequestEvent, 'request' | 'url' | 'isSubRequest'>): boolean {
	if (SAFE_METHODS.has(event.request.method) || event.isSubRequest) return true;
	const origin = event.request.headers.get('origin');
	if (origin) return origin === event.url.origin;
	return event.request.headers.get('sec-fetch-site') === 'same-origin';
}

function plain(status: number, message: string): Response {
	return new Response(message, {
		status,
		headers: { 'content-type': 'text/plain; charset=utf-8', 'cache-control': 'no-store' }
	});
}

/** Optional behaviour of one forward (ISFC sessions): see `proxyAdminApi`. */
export interface ForwardHooks {
	/**
	 * Called once when the gateway says the access token is no longer valid (`GW_002`, `GW_003`,
	 * `GW_004`). Returns the refreshed token, or null when the session could not be refreshed.
	 */
	refreshToken?: () => Promise<string | null>;
	/**
	 * The gateway echoed the tenant it used (`X-Isfc-Tenant-Id`) for a request that named none
	 * (it fell back to the user's top tenant): the session should adopt it. Never called when the
	 * request carried an explicit tenant.
	 */
	onTenantEcho?: (tenantId: string) => void;
}

/** Forwards one admin API request upstream with the session token. Never throws. */
export async function forwardToAdmin(
	event: Pick<RequestEvent, 'request' | 'url' | 'isSubRequest' | 'locals'>,
	upstream: string,
	fetchImpl: typeof fetch = fetch,
	hooks: ForwardHooks = {}
): Promise<Response> {
	if (!sameOrigin(event)) return plain(403, 'Cross-origin admin API call refused.');
	const { request, url } = event;
	const headers = new Headers();
	for (const name of REQUEST_HEADERS) {
		const v = request.headers.get(name);
		if (v !== null) headers.set(name, v);
	}
	// Always the session's token (or none): a header sent by the browser is never passed on.
	const token = event.locals.session?.accessToken;
	if (token) headers.set('authorization', `Bearer ${token}`);
	// ISFC: the tenant the operator works in (HUB-494). The gateway checks the user may enter it
	// (403 GW_015 otherwise) and sends the Hub that tenant's roles.
	const tenant = token ? (event.locals.session?.tenantId ?? null) : null;
	if (tenant && isTenantUuid(tenant)) headers.set('x-tenant-id', tenant);

	const body = SAFE_METHODS.has(request.method) ? undefined : await request.arrayBuffer();
	const send = () =>
		fetchImpl(`${upstream}${url.pathname}${url.search}`, {
			method: request.method,
			headers,
			body,
			redirect: 'manual',
			signal: request.signal
		});
	let res: Response;
	try {
		res = await send();
		// ISFC: the access token expired or was refused. Refresh it once and repeat the call (the
		// body was read above, so it can be sent again). A second refusal is the answer.
		if (res.status === 401 && hooks.refreshToken) {
			const refused = await res
				.clone()
				.json()
				.then((b: unknown) => isTokenRefusal(res.status, b))
				.catch(() => false);
			if (refused) {
				const fresh = await hooks.refreshToken();
				if (fresh) {
					headers.set('authorization', `Bearer ${fresh}`);
					res = await send();
				}
			}
		}
	} catch {
		return plain(
			502,
			`The admin API at ${upstream} did not answer. Is the hub admin role running?`
		);
	}
	const echoed = res.headers.get('x-isfc-tenant-id')?.trim().toLowerCase();
	if (!tenant && echoed && isTenantUuid(echoed)) hooks.onTenantEcho?.(echoed);
	res = await normalizeGatewayError(res);
	const out = new Headers();
	for (const name of RESPONSE_HEADERS) {
		const v = res.headers.get(name);
		if (v !== null) out.set(name, v);
	}
	// Admin answers are per user and change under the browser (an endpoint added to a vendor keeps
	// the vendor's ETag, which follows its revision): never let the browser keep one and revalidate
	// it. SvelteKit answers a matching If-None-Match with 304 by itself, so a cached copy would
	// otherwise come back stale (HUB-290).
	if (!out.has('cache-control')) out.set('cache-control', 'no-store');
	return new Response(res.body, { status: res.status, statusText: res.statusText, headers: out });
}

/** `handle` step: proxies `/admin/v1/**` when `HUB_ADMIN_UPSTREAM` is set; passes through otherwise. */
export const proxyAdminApi: Handle = async ({ event, resolve }) => {
	if (!isAdminPath(event.url.pathname)) return resolve(event);
	const upstream = adminUpstream();
	if (!upstream) return resolve(event);
	// ISFC sessions refresh once on a gateway "token no longer valid" (HUB-492); the new cookie is
	// set on this response, so the browser keeps the rotated tokens.
	const response = await forwardToAdmin(event, upstream, fetch, {
		onTenantEcho: (tenantId) => {
			const session = event.locals.session;
			const secret = sessionSecret();
			if (!session || session.provider !== 'isfc' || !secret) return;
			event.locals.session = { ...session, tenantId };
			event.locals.sessionDirty = true;
			writeSessionCookie(
				event.cookies,
				event.locals.session,
				secret,
				event.url.protocol === 'https:'
			);
		},
		refreshToken: async () => {
			const outcome = await refreshForEvent(event, { fetch: event.fetch, force: true });
			return outcome === 'refreshed' ? (event.locals.session?.accessToken ?? null) : null;
		}
	});
	// SvelteKit only adds `cookies.set` cookies to responses made by `resolve()`; this one is our
	// own, so a refreshed or switched session must be put on it by hand (HUB-492).
	if (event.locals.sessionDirty) {
		const secure = event.url.protocol === 'https:';
		for (const h of sessionSetCookieHeaders(
			event.cookies,
			event.locals.session,
			sessionSecret(),
			secure
		)) {
			response.headers.append('set-cookie', h);
		}
	}
	return response;
};

/**
 * Response headers of admin API calls made by universal `load` functions that SvelteKit must
 * serialize for hydration. `adminClient().call` reads the request id and ETag of every response;
 * without this list SvelteKit throws on SSR ("Failed to get response header", HUB-289), which
 * mock mode never showed because it renders client-side only.
 */
export const SERIALIZED_ADMIN_HEADERS: readonly string[] = [
	REQUEST_ID_HEADER.toLowerCase(),
	ETAG_HEADER.toLowerCase()
];

export function filterSerializedResponseHeaders(name: string): boolean {
	return SERIALIZED_ADMIN_HEADERS.includes(name.toLowerCase());
}

/** `handle` step: lets SSR loads read the admin headers above (see `SERIALIZED_ADMIN_HEADERS`). */
export const serializeAdminHeaders: Handle = ({ event, resolve }) =>
	resolve(event, { filterSerializedResponseHeaders });

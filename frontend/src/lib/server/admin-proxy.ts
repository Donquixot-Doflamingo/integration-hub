import type { Handle, RequestEvent } from '@sveltejs/kit';
import { env } from '$env/dynamic/private';
import { DEFAULT_ADMIN_API_BASE, ETAG_HEADER, REQUEST_ID_HEADER } from '$lib/api/client';

/**
 * Same-origin proxy for the admin API (HUB-286, ADR-0019).
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

/** Forwards one admin API request upstream with the session token. Never throws. */
export async function forwardToAdmin(
	event: Pick<RequestEvent, 'request' | 'url' | 'isSubRequest' | 'locals'>,
	upstream: string,
	fetchImpl: typeof fetch = fetch
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

	const body = SAFE_METHODS.has(request.method) ? undefined : await request.arrayBuffer();
	let res: Response;
	try {
		res = await fetchImpl(`${upstream}${url.pathname}${url.search}`, {
			method: request.method,
			headers,
			body,
			redirect: 'manual',
			signal: request.signal
		});
	} catch {
		return plain(
			502,
			`The admin API at ${upstream} did not answer. Is the hub admin role running?`
		);
	}
	const out = new Headers();
	for (const name of RESPONSE_HEADERS) {
		const v = res.headers.get(name);
		if (v !== null) out.set(name, v);
	}
	return new Response(res.body, { status: res.status, statusText: res.statusText, headers: out });
}

/** `handle` step: proxies `/admin/v1/**` when `HUB_ADMIN_UPSTREAM` is set; passes through otherwise. */
export const proxyAdminApi: Handle = async ({ event, resolve }) => {
	if (!isAdminPath(event.url.pathname)) return resolve(event);
	const upstream = adminUpstream();
	if (!upstream) return resolve(event);
	return forwardToAdmin(event, upstream);
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

/**
 * Post-login destinations. `next` comes from the query string, so it is untrusted: only
 * same-origin absolute paths are kept (no `//host`, no `/\host`, no scheme, no control characters).
 */
export const LOGIN_PATH = '/login';

export function safeNext(next: string | null | undefined, fallback = '/'): string {
	if (!next || next.length > 2048) return fallback;
	if (!next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\')) return fallback;
	// eslint-disable-next-line no-control-regex
	if (/[\u0000-\u001f\u007f]/.test(next)) return fallback;
	try {
		const base = 'http://console.invalid';
		const u = new URL(next, base);
		if (u.origin !== base) return fallback;
		return u.pathname + u.search + u.hash;
	} catch {
		return fallback;
	}
}

/** `/login?next=<current path>` (plus `expired=1` when the API rejected the session). */
export function loginUrl(from: URL, opts: { expired?: boolean } = {}): string {
	const params = new URLSearchParams();
	const next = from.pathname + from.search;
	if (next !== '/') params.set('next', next);
	if (opts.expired) params.set('expired', '1');
	const qs = params.toString();
	return qs ? `${LOGIN_PATH}?${qs}` : LOGIN_PATH;
}

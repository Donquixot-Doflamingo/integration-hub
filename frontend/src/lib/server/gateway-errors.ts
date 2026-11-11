import { isfcErrorMessage } from '$lib/auth/isfc-errors';

/**
 * ISFC's api-gateway (and auth-service behind it) answers errors as
 * `{ error_code, error, message }`, not as the Hub's `{ error: { code, message, ... } }`. The
 * console's API client only understands the Hub envelope, so the admin proxy rewrites a
 * gateway error into it, with a fixed message per ISFC code (never ISFC's raw text): a `GW_015`
 * ("no access to that tenant") then reads as such instead of "unexpected response" (HUB-492,
 * HUB-494). A Hub envelope, and anything that is not an ISFC error, passes through untouched.
 */
const CODE_BY_STATUS: Record<number, string> = {
	400: 'VALIDATION_FAILED',
	401: 'UNAUTHORIZED',
	403: 'FORBIDDEN',
	429: 'RATE_LIMITED'
};

/** ISFC code of an error body (`GW_015`, `AS_FB_21`), or null when it is not an ISFC error. */
export function isfcCodeOf(body: unknown): string | null {
	if (typeof body !== 'object' || body === null) return null;
	const b = body as { error_code?: unknown; error?: unknown };
	// A Hub envelope has `error` as an object with a `code`: not ours to rewrite.
	if (typeof b.error === 'object' && b.error !== null) return null;
	return typeof b.error_code === 'string' && /^(?:GW_\d{3}|AS_[A-Z]{2}_\d{2})$/.test(b.error_code)
		? b.error_code
		: null;
}

export async function normalizeGatewayError(res: Response): Promise<Response> {
	if (res.ok || !res.headers.get('content-type')?.includes('json')) return res;
	let body: unknown;
	try {
		body = await res.clone().json();
	} catch {
		return res;
	}
	const code = isfcCodeOf(body);
	if (!code) return res;
	const status = res.status;
	const hubCode = CODE_BY_STATUS[status] ?? 'INTERNAL_ERROR';
	const requestId =
		res.headers.get('x-hub-request-id') ??
		(typeof (body as { request_id?: unknown }).request_id === 'string'
			? (body as { request_id: string }).request_id
			: '');
	const headers = new Headers({
		'content-type': 'application/json',
		'cache-control': 'no-store'
	});
	if (requestId) headers.set('x-hub-request-id', requestId);
	const retryAfter = res.headers.get('retry-after');
	if (retryAfter) headers.set('retry-after', retryAfter);
	return new Response(
		JSON.stringify({
			error: {
				code: hubCode,
				message:
					isfcErrorMessage(code) ??
					(status === 401
						? 'Your sign-in is not valid. Sign in again.'
						: 'ISFC refused the request.'),
				retryable: status === 429 || status === 502 || status === 503 || status === 504,
				request_id: requestId,
				details: [],
				meta: { isfc_error_code: code }
			}
		}),
		{ status, headers }
	);
}

/** True for the gateway answers that mean "this access token is no longer good" (refresh once). */
export function isTokenRefusal(status: number, body: unknown): boolean {
	const code = isfcCodeOf(body);
	return status === 401 && (code === 'GW_002' || code === 'GW_003' || code === 'GW_004');
}

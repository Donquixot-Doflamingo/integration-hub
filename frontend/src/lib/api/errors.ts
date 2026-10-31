import type { components } from './schema';

/** The canonical admin error object (`AdminError` in hub-admin.v1.yaml). */
export type AdminError = components['schemas']['AdminError'];
/** `{ "error": AdminError }`, returned by every admin 4xx/5xx. */
export type AdminErrorEnvelope = components['schemas']['AdminErrorEnvelope'];
/** Error codes the admin contract defines. */
export type AdminErrorCode = AdminError['code'];

/**
 * Codes the console adds for failures that never reached (or never came from) the Hub:
 * - `NETWORK_ERROR`: fetch threw (offline, DNS, CORS, aborted). `status` is 0.
 * - `UNEXPECTED_RESPONSE`: a non-2xx without the canonical envelope (proxy 502 page, HTML, empty body).
 */
export type ClientErrorCode = 'NETWORK_ERROR' | 'UNEXPECTED_RESPONSE';

/**
 * Any code a `HubApiError` can carry. Unknown server codes are kept verbatim (the contract says
 * clients must tolerate new codes and fall back to HTTP status + `retryable`).
 */
export type HubErrorCode = AdminErrorCode | ClientErrorCode | (string & {});

const RETRYABLE_STATUSES = new Set([0, 429, 502, 503, 504]);

export function isAdminErrorEnvelope(body: unknown): body is AdminErrorEnvelope {
	if (typeof body !== 'object' || body === null || !('error' in body)) return false;
	const e = (body as { error: unknown }).error;
	return (
		typeof e === 'object' &&
		e !== null &&
		typeof (e as { code?: unknown }).code === 'string' &&
		typeof (e as { message?: unknown }).message === 'string'
	);
}

/** Typed error for every failed admin API call. Read `code`, never match on `message`. */
export class HubApiError extends Error {
	override readonly name = 'HubApiError';
	/** HTTP status; 0 for network failures. */
	readonly status: number;
	readonly code: HubErrorCode;
	readonly retryable: boolean;
	readonly retryAfterMs: number | null;
	/** `req_...` id to quote to the platform team. Body `request_id` first, then the response header. */
	readonly requestId: string | null;
	readonly details: NonNullable<AdminError['details']>;
	readonly meta: AdminError['meta'] | null;
	/** The canonical error object when the server sent one. */
	readonly envelope: AdminError | null;

	constructor(init: {
		status: number;
		code: HubErrorCode;
		message: string;
		retryable?: boolean;
		retryAfterMs?: number | null;
		requestId?: string | null;
		details?: AdminError['details'];
		meta?: AdminError['meta'] | null;
		envelope?: AdminError | null;
		cause?: unknown;
	}) {
		super(init.message, init.cause === undefined ? undefined : { cause: init.cause });
		this.status = init.status;
		this.code = init.code;
		this.retryable = init.retryable ?? RETRYABLE_STATUSES.has(init.status);
		this.retryAfterMs = init.retryAfterMs ?? null;
		this.requestId = init.requestId ?? null;
		this.details = init.details ?? [];
		this.meta = init.meta ?? null;
		this.envelope = init.envelope ?? null;
	}

	/** Builds the error from a non-2xx response body (parsed JSON, or anything else). */
	static fromResponse(body: unknown, response: Response, headerRequestId: string | null) {
		if (isAdminErrorEnvelope(body)) {
			const e = body.error;
			return new HubApiError({
				status: response.status,
				code: e.code,
				message: e.message,
				retryable: typeof e.retryable === 'boolean' ? e.retryable : undefined,
				retryAfterMs: e.retry_after_ms ?? null,
				requestId: e.request_id || headerRequestId,
				details: e.details,
				meta: e.meta,
				envelope: e
			});
		}
		return new HubApiError({
			status: response.status,
			code: 'UNEXPECTED_RESPONSE',
			message: `Unexpected response from the admin API (HTTP ${response.status}${
				response.statusText ? ` ${response.statusText}` : ''
			})`,
			requestId: headerRequestId
		});
	}

	static network(cause: unknown) {
		return new HubApiError({
			status: 0,
			code: 'NETWORK_ERROR',
			message: 'Could not reach the admin API. Check your connection and try again.',
			retryable: true,
			cause
		});
	}

	/** True when the error carries one of the given codes. */
	is(...codes: HubErrorCode[]): boolean {
		return codes.includes(this.code);
	}
}

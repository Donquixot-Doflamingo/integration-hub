import { HubApiError, type AdminError, type HubErrorCode } from './errors';

/**
 * What SvelteKit form actions (mutations, HUB-259+) hand back to the page. Action data must be
 * plain (it is serialised with devalue), so a `HubApiError` travels as `PlainApiError` and is
 * rebuilt in the browser for `ErrorPanel`.
 */
export interface PlainApiError {
	status: number;
	code: HubErrorCode;
	message: string;
	retryable: boolean;
	retryAfterMs: number | null;
	requestId: string | null;
	details: NonNullable<AdminError['details']>;
	meta: AdminError['meta'] | null;
}

export function toPlainError(e: HubApiError): PlainApiError {
	return {
		status: e.status,
		code: e.code,
		message: e.message,
		retryable: e.retryable,
		retryAfterMs: e.retryAfterMs,
		requestId: e.requestId,
		details: e.details,
		meta: e.meta
	};
}

export function fromPlainError(p: PlainApiError): HubApiError {
	return new HubApiError(p);
}

/** Form fields an action can blame (`reason`, `spec`, `weights`, `confirm`, ...). */
export type FieldErrors = Partial<Record<string, string>>;

/** `fail(status, data)` payload of every console action. */
export interface ActionFailureData {
	/** Which action failed (`publish`, `rollback`, ...), so one page can host several. */
	op: string;
	/** The admin API's answer, when the call was made. */
	error?: PlainApiError;
	/** Problems the console found before calling the API, or API pointers mapped to fields. */
	fields?: FieldErrors;
}

/** Success payload: the change log id plus the operation's own result. */
export interface ActionSuccessData<T = unknown> {
	op: string;
	changeId: string | null;
	/** `X-Hub-Request-Id` of the call that made the change, to look it up in the request explorer. */
	requestId: string | null;
	result: T;
}

/** True for answers that mean "someone else changed this first" (reload, then retry). */
export function isConcurrencyError(e: { code: HubErrorCode }): boolean {
	return e.code === 'CONFLICT' || e.code === 'PRECONDITION_FAILED';
}

/**
 * Splits `VALIDATION_FAILED` details into form fields. Pointers are relative to the request body
 * (`/reason`, `/spec/targets/0/weight`); a pointer whose first token is in `fields` is blamed on
 * that field; everything else stays in the returned `rest`.
 */
export function detailsToFields(
	details: PlainApiError['details'],
	fields: readonly string[]
): { fields: FieldErrors; rest: PlainApiError['details'] } {
	const out: FieldErrors = {};
	const rest: PlainApiError['details'] = [];
	for (const d of details) {
		const first = d.pointer?.split('/')[1];
		if (first && fields.includes(first) && !out[first]) out[first] = d.message;
		else rest.push(d);
	}
	return { fields: out, rest };
}

import { fail, redirect, type ActionFailure, type RequestEvent } from '@sveltejs/kit';
import type { ApiResult } from '$lib/api/client';
import { consoleApi } from '$lib/api/console';
import {
	detailsToFields,
	toPlainError,
	type ActionFailureData,
	type ActionSuccessData,
	type FieldErrors
} from '$lib/api/actions';
import { loginUrl } from '$lib/auth/redirect';
import { reasonProblem } from '$lib/reason';

/**
 * Shared plumbing for console mutations (HUB-259+). Every mutation is a SvelteKit form action:
 * it runs on the server, so the admin bearer token (added by `handleFetch`) never reaches the
 * browser, and the page uses `use:enhance` for the dialog flow (ADR-0009: allowed now that `//`
 * paths are rejected first in `hooks.server.ts`).
 */

/** Actions do not run the `(app)` layout load, so each one checks the session itself. */
export function requireSession(event: RequestEvent): void {
	// Back to the page, not to the `?/action` URL, after signing in again.
	const page = new URL(event.url.pathname, event.url);
	if (!event.locals.session) redirect(303, loginUrl(page, { expired: true }));
}

/** The admin client for an action (SvelteKit `fetch`, token attached by `handleFetch`). */
export function actionApi(event: RequestEvent) {
	return consoleApi(event);
}

export type Form = FormData;

export function text(form: Form, name: string): string {
	const v = form.get(name);
	return typeof v === 'string' ? v : '';
}

/** An integer form field, or null when missing or not an integer. */
export function int(form: Form, name: string): number | null {
	const raw = text(form, name).trim();
	if (!/^-?\d+$/.test(raw)) return null;
	return Number(raw);
}

/** `fail(400)` for problems found before calling the API (no request was made). */
export function invalid(op: string, fields: FieldErrors) {
	return fail(400, { op, fields } satisfies ActionFailureData);
}

/** Validates the mandatory change reason; returns the trimmed reason or a `fail(400)`. */
export function readReason(op: string, form: Form) {
	const reason = text(form, 'reason').trim();
	const problem = reasonProblem(reason);
	return problem
		? { ok: false as const, failure: invalid(op, { reason: problem }) }
		: { ok: true as const, reason };
}

/**
 * Turns an admin API answer into the action's return value: success data with the `change_id`
 * (null for non-mutating operations such as goldens or dry runs), or `fail(status)` with the error
 * (VALIDATION_FAILED pointers into `fieldNames` become fields).
 */
export function finish<T extends object>(
	op: string,
	res: ApiResult<T>,
	fieldNames: readonly string[] = ['reason']
): ActionSuccessData<T> | ActionFailure<ActionFailureData> {
	if (res.ok) {
		const changeId = 'change_id' in res.data ? (res.data.change_id as string | null) : null;
		return { op, changeId: changeId ?? null, requestId: res.requestId, result: res.data };
	}
	const error = toPlainError(res.error);
	const { fields } = detailsToFields(error.details, fieldNames);
	// Status 0 (network) is not a valid HTTP status for `fail`; answer 502 (bad gateway).
	return fail(res.status >= 400 ? res.status : 502, { op, error, fields });
}

import type { FieldErrors } from '$lib/api/actions';
import type { RateLimit, Tenant, TenantCreate, TenantUpdate } from '$lib/api/types';

/**
 * Tenant create / edit form (HUB-270), checked against the contract before anything is sent:
 * `TenantCreate` / `TenantUpdate` in hub-admin.v1.yaml. The dialog runs the same checks in the
 * browser (Confirm stays disabled) and the form action runs them again for the no-JS path.
 * - `tenant_id`: `Slug` (`^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$`), create only (it never changes).
 * - `display_name`: required on create, at most 128 characters; cannot be emptied on edit.
 * - `description`, `contact`: nullable strings; empty means null (cleared).
 * - `rate_limit.rps`: number >= 0; `rate_limit.burst`: integer >= 0. Both optional. `RateLimit`
 *   fields are not nullable and the edit is a merge-patch, so an empty field keeps the current
 *   value; the API has no way to remove a limit once set.
 */
export const SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/;
export const DISPLAY_NAME_MAX = 128;

/** Raw form input (strings as typed). */
export interface FieldValues {
	tenant_id: string;
	display_name: string;
	description: string;
	contact: string;
	rps: string;
	burst: string;
}

export const EMPTY_VALUES: FieldValues = {
	tenant_id: '',
	display_name: '',
	description: '',
	contact: '',
	rps: '',
	burst: ''
};

/** Form names of the fields, in order (also the keys of `FieldValues`). */
export const FIELD_NAMES = Object.keys(EMPTY_VALUES) as (keyof FieldValues)[];

/**
 * Fields an API `VALIDATION_FAILED` pointer may blame (first pointer token): `/rate_limit/rps`
 * lands on the rate limit group.
 */
export const API_FIELDS = [
	'reason',
	'tenant_id',
	'display_name',
	'description',
	'contact',
	'rate_limit'
] as const;

export function valuesFromTenant(t: Tenant): FieldValues {
	return {
		tenant_id: t.tenant_id,
		display_name: t.display_name,
		description: t.description ?? '',
		contact: t.contact ?? '',
		rps: t.rate_limit.rps == null ? '' : String(t.rate_limit.rps),
		burst: t.rate_limit.burst == null ? '' : String(t.rate_limit.burst)
	};
}

/**
 * The values posted by the dialog. `prefix` reads the hidden copy of the values the edit dialog
 * opened with (`before.display_name`, ...), so the action sends only what the operator changed.
 */
export const BEFORE_PREFIX = 'before.';

export function valuesFromForm(form: FormData, prefix = ''): FieldValues {
	const out = { ...EMPTY_VALUES };
	for (const key of FIELD_NAMES) {
		const v = form.get(prefix + key);
		out[key] = typeof v === 'string' ? v : '';
	}
	return out;
}

const NUMBER = /^\d+(\.\d+)?$/;
const INTEGER = /^\d+$/;

/** Problems with the values, by field name; empty when the form can be sent. */
export function problems(v: FieldValues, mode: 'create' | 'edit' | 'setup'): FieldErrors {
	const out: FieldErrors = {};
	if (mode === 'create') {
		const id = v.tenant_id.trim();
		if (!id) out.tenant_id = 'Enter a tenant id.';
		else if (!SLUG.test(id))
			out.tenant_id =
				'3 to 64 characters: letters, digits, "_" or "-", starting with a letter or digit.';
	}
	const name = v.display_name.trim();
	if (!name) out.display_name = 'Enter a display name.';
	else if (name.length > DISPLAY_NAME_MAX)
		out.display_name = `Use at most ${DISPLAY_NAME_MAX} characters (${name.length} now).`;
	const rps = v.rps.trim();
	if (rps && !NUMBER.test(rps)) out.rps = 'A number of requests per second, 0 or more.';
	const burst = v.burst.trim();
	if (burst && !INTEGER.test(burst)) out.burst = 'A whole number of requests, 0 or more.';
	return out;
}

function rateLimit(v: FieldValues): RateLimit | undefined {
	const out: RateLimit = {};
	if (v.rps.trim()) out.rps = Number(v.rps.trim());
	if (v.burst.trim()) out.burst = Number(v.burst.trim());
	return Object.keys(out).length ? out : undefined;
}

const orNull = (s: string) => (s.trim() ? s.trim() : null);

export type Built<T> = { ok: true; body: T } | { ok: false; fields: FieldErrors };

export function createBody(v: FieldValues, reason: string): Built<TenantCreate> {
	const fields = problems(v, 'create');
	if (Object.keys(fields).length) return { ok: false, fields };
	const body: TenantCreate = {
		tenant_id: v.tenant_id.trim(),
		display_name: v.display_name.trim(),
		description: orNull(v.description),
		contact: orNull(v.contact),
		reason
	};
	const rl = rateLimit(v);
	if (rl) body.rate_limit = rl;
	return { ok: true, body };
}

/**
 * Merge-patch body with only the fields that differ from `before` (the values the dialog opened
 * with). Nothing changed is a form problem, not an empty PATCH.
 */
export function updateBody(
	v: FieldValues,
	before: FieldValues,
	reason: string
): Built<TenantUpdate> {
	const fields = problems(v, 'edit');
	if (Object.keys(fields).length) return { ok: false, fields };
	const body: TenantUpdate = { reason };
	if (v.display_name.trim() !== before.display_name.trim())
		body.display_name = v.display_name.trim();
	if (v.description.trim() !== before.description.trim()) body.description = orNull(v.description);
	if (v.contact.trim() !== before.contact.trim()) body.contact = orNull(v.contact);
	const rl: RateLimit = {};
	if (v.rps.trim() && Number(v.rps.trim()) !== Number(before.rps.trim() || NaN))
		rl.rps = Number(v.rps.trim());
	if (v.burst.trim() && Number(v.burst.trim()) !== Number(before.burst.trim() || NaN))
		rl.burst = Number(v.burst.trim());
	if (Object.keys(rl).length) body.rate_limit = rl;
	if (Object.keys(body).length === 1) {
		return { ok: false, fields: { display_name: 'Nothing changed. Edit at least one field.' } };
	}
	return { ok: true, body };
}

/**
 * "Complete setup" (HUB-485, tenant_admin): the same merge-patch as an edit, plus `setup:
 * 'complete'`. Unlike `updateBody`, nothing changed is fine: a tenant that is right as synced from
 * ISFC is finished with just the reason. There is no way back to `pending` (contract).
 */
export function setupBody(
	v: FieldValues,
	before: FieldValues,
	reason: string
): Built<TenantUpdate> {
	const fields = problems(v, 'setup');
	if (Object.keys(fields).length) return { ok: false, fields };
	const edit = updateBody(v, before, reason);
	const body: TenantUpdate = edit.ok ? edit.body : { reason };
	body.setup = 'complete';
	return { ok: true, body };
}

/** True when `updateBody` would send something (the dialog's Confirm). */
export function hasChanges(v: FieldValues, before: FieldValues): boolean {
	const r = updateBody(v, before, 'x');
	return r.ok;
}

/** The dialog's Confirm: no problems, and (edit) something changed. */
export function canSend(
	v: FieldValues,
	before: FieldValues,
	mode: 'create' | 'edit' | 'setup'
): boolean {
	if (Object.keys(problems(v, mode)).length) return false;
	return mode !== 'edit' || hasChanges(v, before);
}

/** "200 requests/s, burst 400" / "No limit set". */
export function rateLimitText(rl: RateLimit): string {
	const parts: string[] = [];
	if (rl.rps != null) parts.push(`${rl.rps} requests/s`);
	if (rl.burst != null) parts.push(`burst ${rl.burst}`);
	return parts.length ? parts.join(', ') : 'No limit set';
}

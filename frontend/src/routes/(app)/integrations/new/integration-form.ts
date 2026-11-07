import type { FieldErrors } from '$lib/api/actions';
import type { IntegrationCreate } from '$lib/api/types';

/**
 * Create integration form (HUB-292), checked against `IntegrationCreate` before sending. The id is
 * what callers put in the path (`POST /v1/integrations/{id}/invoke`) and never changes; vendors,
 * accounts and versions stay behind it (D1, D12). `interactive` is fixed at creation too. The
 * operations and their request schemas belong to versions (ADR-0023): the first draft sets them. Errors are keyed by JSON pointer.
 */
export const SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/;
export const DESCRIPTION_MAX = 1024;

export interface FormValues {
	integration_id: string;
	owner_tenant: string;
	description: string;
	/** Comma- or space-separated tenant ids. */
	grants: string;
	/** Operations are steps of one session (`X-Hub-Session`), sync only. Fixed at creation. */
	interactive: boolean;
	moves_money_or_quota: boolean;
}

export const POINTERS = [
	'/integration_id',
	'/owner_tenant',
	'/description',
	'/grants',
	'/interactive',
	'/moves_money_or_quota'
] as const;

export function emptyValues(): FormValues {
	return {
		integration_id: '',
		owner_tenant: '',
		description: '',
		grants: '',
		interactive: false,
		moves_money_or_quota: false
	};
}

export function valuesFromForm(form: FormData): FormValues {
	const get = (n: string) => {
		const v = form.get(n);
		return typeof v === 'string' ? v : '';
	};
	const on = (n: string) => get(n) === 'on' || get(n) === 'true';
	return {
		integration_id: get('integration_id'),
		owner_tenant: get('owner_tenant'),
		description: get('description'),
		grants: get('grants'),
		interactive: on('interactive'),
		moves_money_or_quota: on('moves_money_or_quota')
	};
}

export function grantList(raw: string): string[] {
	return [
		...new Set(
			raw
				.split(/[\s,]+/)
				.map((s) => s.trim())
				.filter(Boolean)
		)
	];
}

export function problems(v: FormValues): FieldErrors {
	const out: FieldErrors = {};
	const id = v.integration_id.trim();
	if (!id) out['/integration_id'] = 'Enter an integration id.';
	else if (!SLUG.test(id))
		out['/integration_id'] =
			'3 to 64 characters: letters, digits, "_" or "-", starting with a letter or digit.';
	if (!v.owner_tenant) out['/owner_tenant'] = 'Pick the tenant that owns it.';
	if (v.description.length > DESCRIPTION_MAX)
		out['/description'] = `Use at most ${DESCRIPTION_MAX} characters.`;
	const bad = grantList(v.grants).find((g) => !SLUG.test(g));
	if (bad) out['/grants'] = `"${bad}" is not a tenant id.`;
	else if (grantList(v.grants).includes(v.owner_tenant))
		out['/grants'] = 'The owner can always call it; list other tenants only.';
	return out;
}

export function createBody(
	v: FormValues,
	reason: string
): { ok: true; body: IntegrationCreate } | { ok: false; fields: FieldErrors } {
	const found = problems(v);
	if (Object.keys(found).length) return { ok: false, fields: found };
	return {
		ok: true,
		body: {
			integration_id: v.integration_id.trim(),
			owner_tenant: v.owner_tenant,
			description: v.description.trim() || null,
			grants: grantList(v.grants),
			interactive: v.interactive,
			moves_money_or_quota: v.moves_money_or_quota,
			reason
		}
	};
}

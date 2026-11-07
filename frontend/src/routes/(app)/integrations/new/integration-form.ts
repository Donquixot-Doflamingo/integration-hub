import type { FieldErrors } from '$lib/api/actions';
import type { IntegrationCreate } from '$lib/api/types';

/**
 * Create integration form (HUB-292), checked against `IntegrationCreate` before sending. The id is
 * what callers put in the path (`POST /v1/integrations/{id}/invoke`) and never changes; vendors,
 * accounts and versions stay behind it (D1, D12). Errors are keyed by JSON pointer.
 */
export const SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/;
export const CAPABILITY = /^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+@v[1-9][0-9]*$/;
export const DESCRIPTION_MAX = 1024;

export interface FormValues {
	integration_id: string;
	owner_tenant: string;
	capability: string;
	description: string;
	/** Comma- or space-separated tenant ids. */
	grants: string;
	moves_money_or_quota: boolean;
}

export const POINTERS = [
	'/integration_id',
	'/owner_tenant',
	'/capability',
	'/description',
	'/grants',
	'/moves_money_or_quota'
] as const;

export function emptyValues(): FormValues {
	return {
		integration_id: '',
		owner_tenant: '',
		capability: '',
		description: '',
		grants: '',
		moves_money_or_quota: false
	};
}

export function valuesFromForm(form: FormData): FormValues {
	const get = (n: string) => {
		const v = form.get(n);
		return typeof v === 'string' ? v : '';
	};
	return {
		integration_id: get('integration_id'),
		owner_tenant: get('owner_tenant'),
		capability: get('capability'),
		description: get('description'),
		grants: get('grants'),
		moves_money_or_quota:
			get('moves_money_or_quota') === 'on' || get('moves_money_or_quota') === 'true'
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
	if (!v.capability) out['/capability'] = 'Pick the capability.';
	else if (!CAPABILITY.test(v.capability))
		out['/capability'] = 'A capability key such as events.track@v1.';
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
			capability: v.capability,
			description: v.description.trim() || null,
			grants: grantList(v.grants),
			moves_money_or_quota: v.moves_money_or_quota,
			reason
		}
	};
}

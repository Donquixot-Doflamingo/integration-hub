import type { FieldErrors } from '$lib/api/actions';
import type {
	RegistrarEndpoint,
	RegistrarEndpointCreate,
	RegistrarEndpointUpdate
} from '$lib/api/types';
import { formatUtc } from '$lib/utils/format';
import { SLUG } from '../tenant-form';

/**
 * Registrar (callback) endpoints of a tenant (HUB-272), against `RegistrarEndpointCreate` /
 * `RegistrarEndpointUpdate`: `endpoint_id` is a `Slug`, `url` an `https://` URI. The Hub's SSRF
 * guard (host allowlist, no private or link-local ranges) runs only on the server; its 400
 * `VALIDATION_FAILED` pointer `/url` lands on the URL field.
 *
 * Signing secrets are write-only: no read returns them. Create and rotate answer with the new
 * `signing_secret` once; the console shows it once in `SecretOnce` (like an API key) and never
 * again: the list shows only the secret version.
 */
export interface EndpointValues {
	endpoint_id: string;
	url: string;
	description: string;
}

export const API_FIELDS = ['reason', 'endpoint_id', 'url', 'description'] as const;

export function endpointValuesFromForm(form: FormData, prefix = ''): EndpointValues {
	const str = (k: string) => {
		const v = form.get(prefix + k);
		return typeof v === 'string' ? v : '';
	};
	return { endpoint_id: str('endpoint_id'), url: str('url'), description: str('description') };
}

export function urlProblem(raw: string): string | null {
	const v = raw.trim();
	if (!v) return 'Enter the callback URL.';
	if (!/^https:\/\//i.test(v)) return 'Use an https:// URL.';
	try {
		const u = new URL(v);
		if (!u.hostname) return 'Enter a URL with a host name.';
	} catch {
		return 'Enter a valid URL, e.g. https://orders.internal/hub/callbacks.';
	}
	return null;
}

export function endpointProblems(v: EndpointValues, mode: 'create' | 'edit'): FieldErrors {
	const out: FieldErrors = {};
	if (mode === 'create') {
		const id = v.endpoint_id.trim();
		if (!id) out.endpoint_id = 'Enter an endpoint id.';
		else if (!SLUG.test(id))
			out.endpoint_id =
				'3 to 64 characters: letters, digits, "_" or "-", starting with a letter or digit.';
	}
	const url = urlProblem(v.url);
	if (url) out.url = url;
	return out;
}

type Built<T> = { ok: true; body: T } | { ok: false; fields: FieldErrors };

export function createEndpointBody(
	tenantId: string,
	v: EndpointValues,
	reason: string
): Built<RegistrarEndpointCreate> {
	const fields = endpointProblems(v, 'create');
	if (Object.keys(fields).length) return { ok: false, fields };
	return {
		ok: true,
		body: {
			endpoint_id: v.endpoint_id.trim(),
			tenant_id: tenantId,
			url: v.url.trim(),
			description: v.description.trim() || null,
			reason
		}
	};
}

/** Merge-patch of URL and description: only what changed versus `before`. */
export function updateEndpointBody(
	v: EndpointValues,
	before: EndpointValues,
	reason: string
): Built<RegistrarEndpointUpdate> {
	const fields = endpointProblems(v, 'edit');
	if (Object.keys(fields).length) return { ok: false, fields };
	const body: RegistrarEndpointUpdate = { reason };
	if (v.url.trim() !== before.url.trim()) body.url = v.url.trim();
	if (v.description.trim() !== before.description.trim())
		body.description = v.description.trim() || null;
	if (Object.keys(body).length === 1) {
		return { ok: false, fields: { url: 'Nothing changed. Edit the URL or the description.' } };
	}
	return { ok: true, body };
}

export function valuesFromEndpoint(e: RegistrarEndpoint): EndpointValues {
	return { endpoint_id: e.endpoint_id, url: e.url, description: e.description ?? '' };
}

/** "Version 2; version 1 is also accepted until 2026-11-01 10:00 UTC". */
export function secretText(e: RegistrarEndpoint): string {
	const base = `Version ${e.secret_version}`;
	if (!e.secret_overlap_until) return base;
	return `${base}; callbacks are also signed with version ${e.secret_version - 1} until ${formatUtc(e.secret_overlap_until)}`;
}

/** Signing-secret rotation overlaps (contract `SecretRotate.overlap_ms`, 0 to 30 days). */
export const SECRET_OVERLAPS = [
	{ ms: 0, label: 'None: only the new secret signs from now on' },
	{ ms: 3_600_000, label: '1 hour' },
	{ ms: 86_400_000, label: '24 hours' },
	{ ms: 7 * 86_400_000, label: '7 days' },
	{ ms: 30 * 86_400_000, label: '30 days (maximum)' }
] as const;

/** `SecretOnce` text for a signing secret (HUB-272): what it is for and that it cannot come back. */
export const SIGNING_SECRET_WARNING =
	'The Hub never returns it again: nobody can show it again, not even an admin. Give it to the tenant over a secure channel now: their receiver uses it to verify the X-Hub-Signature header of every callback. If it is lost, rotate the secret.';

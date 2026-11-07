import type { FieldErrors } from '$lib/api/actions';
import type { VendorEndpoint, VendorEndpointCreate } from '$lib/api/types';

/**
 * Vendor endpoint form (HUB-290), checked against `VendorEndpointCreate`. Endpoint versions are
 * immutable: "editing" an endpoint creates the next version of the same name (`upload@2`), so
 * the form starts from the latest version and keeps its name. Errors are keyed by pointer.
 */
export const ENDPOINT_NAME = /^[a-z][a-z0-9_]{0,63}$/;
export const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE'] as const;

export interface FormValues {
	name: string;
	method: string;
	path: string;
	timeout_ms: string;
	connect_timeout_ms: string;
	idempotent: boolean;
	batch_max: string;
	stream: boolean;
}

export function emptyEndpoint(): FormValues {
	return {
		name: '',
		method: 'POST',
		path: '',
		timeout_ms: '3000',
		connect_timeout_ms: '500',
		idempotent: false,
		batch_max: '',
		stream: false
	};
}

export function valuesFromEndpoint(e: VendorEndpoint): FormValues {
	return {
		name: e.name,
		method: e.method ?? 'POST',
		path: e.path ?? '',
		timeout_ms: e.timeout_ms == null ? '' : String(e.timeout_ms),
		connect_timeout_ms: e.connect_timeout_ms == null ? '' : String(e.connect_timeout_ms),
		idempotent: e.idempotent ?? false,
		batch_max: e.batch_max == null ? '' : String(e.batch_max),
		stream: e.stream ?? false
	};
}

const checked = (form: FormData, name: string) => {
	const v = form.get(name);
	return v === 'on' || v === 'true';
};

export function endpointValuesFromForm(form: FormData): FormValues {
	const get = (n: string) => {
		const v = form.get(n);
		return typeof v === 'string' ? v : '';
	};
	return {
		name: get('name'),
		method: get('method'),
		path: get('path'),
		timeout_ms: get('timeout_ms'),
		connect_timeout_ms: get('connect_timeout_ms'),
		idempotent: checked(form, 'idempotent'),
		batch_max: get('batch_max'),
		stream: checked(form, 'stream')
	};
}

export const ENDPOINT_POINTERS = [
	'/name',
	'/method',
	'/path',
	'/timeout_ms',
	'/connect_timeout_ms',
	'/idempotent',
	'/batch_max',
	'/stream'
] as const;

const POSITIVE = /^[1-9]\d*$/;

export function endpointProblems(v: FormValues): FieldErrors {
	const out: FieldErrors = {};
	if (!v.name.trim()) out['/name'] = 'Enter an endpoint name.';
	else if (!ENDPOINT_NAME.test(v.name.trim()))
		out['/name'] = 'Lower-case letters, digits and "_", starting with a letter (e.g. upload).';
	if (!(METHODS as readonly string[]).includes(v.method)) out['/method'] = 'Pick an HTTP method.';
	const path = v.path.trim();
	if (!path) out['/path'] = 'Enter the path on the vendor base URL, e.g. /1/upload.';
	else if (!path.startsWith('/')) out['/path'] = 'Start the path with "/".';
	for (const k of ['timeout_ms', 'connect_timeout_ms'] as const) {
		const raw = v[k].trim();
		if (raw && !POSITIVE.test(raw)) out[`/${k}`] = 'A whole number of milliseconds, at least 1.';
	}
	if (v.batch_max.trim() && !POSITIVE.test(v.batch_max.trim()))
		out['/batch_max'] = 'A whole number of records, at least 1, or empty for no batching.';
	return out;
}

export function createEndpointBody(
	v: FormValues,
	reason: string
): { ok: true; body: VendorEndpointCreate } | { ok: false; fields: FieldErrors } {
	const found = endpointProblems(v);
	if (Object.keys(found).length) return { ok: false, fields: found };
	const body: VendorEndpointCreate = {
		name: v.name.trim(),
		method: v.method as (typeof METHODS)[number],
		path: v.path.trim(),
		idempotent: v.idempotent,
		stream: v.stream,
		batch_max: v.batch_max.trim() ? Number(v.batch_max) : null,
		reason
	};
	if (v.timeout_ms.trim()) body.timeout_ms = Number(v.timeout_ms);
	if (v.connect_timeout_ms.trim()) body.connect_timeout_ms = Number(v.connect_timeout_ms);
	return { ok: true, body };
}

/** The latest version of each endpoint name, by name. */
export function latestByName(endpoints: readonly VendorEndpoint[]): Map<string, VendorEndpoint> {
	const out = new Map<string, VendorEndpoint>();
	for (const e of endpoints) {
		const cur = out.get(e.name);
		if (!cur || e.version > cur.version) out.set(e.name, e);
	}
	return out;
}

/** Endpoints sorted by name, newest version first within a name. */
export function sortEndpoints(endpoints: readonly VendorEndpoint[]): VendorEndpoint[] {
	return [...endpoints].sort((a, b) => a.name.localeCompare(b.name) || b.version - a.version);
}

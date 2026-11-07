import type { FieldErrors } from '$lib/api/actions';
import type { EndpointRequestSchema, VendorEndpoint, VendorEndpointCreate } from '$lib/api/types';
import { schemaDocumentProblem } from '$lib/utils/json-schema';

/**
 * Vendor endpoint form (HUB-290, HUB-436), checked against `VendorEndpointCreate`. Endpoint
 * versions are immutable: "editing" an endpoint creates the next version of the same name
 * (`upload@2`), so the form starts from the latest version and keeps its name. Since hub-admin
 * 2.0.0 (ADR-0023) every endpoint version declares the request it accepts (`request_schema`:
 * path parameters, query parameters, non-credential headers, body; an empty part is not sent)
 * and may document its response (`response_schema`, never enforced). Errors are keyed by pointer.
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
	/** The request this version accepts; a missing part is not sent. */
	request_schema: EndpointRequestSchema;
	/** Informational JSON Schema of the vendor's response body, or null. */
	response_schema: Record<string, unknown> | null;
}

/** The parts of a vendor request, in the order the form shows them. */
export const REQUEST_PARTS = ['path', 'query', 'headers', 'body'] as const;
export type RequestPart = (typeof REQUEST_PARTS)[number];

export function emptyEndpoint(): FormValues {
	return {
		name: '',
		method: 'POST',
		path: '',
		timeout_ms: '3000',
		connect_timeout_ms: '500',
		idempotent: false,
		batch_max: '',
		stream: false,
		request_schema: {},
		response_schema: null
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
		stream: e.stream ?? false,
		request_schema: structuredClone(e.request_schema ?? {}),
		response_schema: e.response_schema ? structuredClone(e.response_schema) : null
	};
}

const isObject = (v: unknown): v is Record<string, unknown> =>
	!!v && typeof v === 'object' && !Array.isArray(v);

/** A JSON object posted as a form field, or null when it is empty or not an object. */
function jsonField(raw: string): Record<string, unknown> | null {
	if (!raw.trim()) return null;
	try {
		const v = JSON.parse(raw) as unknown;
		return isObject(v) ? v : null;
	} catch {
		return null;
	}
}

/** `/users/{id}/orders/{order_id}` -> `['id', 'order_id']`. */
export function pathParams(path: string): string[] {
	return [...new Set([...path.matchAll(/\{([^{}/]+)\}/g)].map((m) => m[1]!.trim()))];
}

/** A path-parameter schema for the placeholders of `path`: every one a required string. */
export function pathSchemaFor(path: string): Record<string, unknown> | null {
	const names = pathParams(path);
	if (!names.length) return null;
	return {
		type: 'object',
		required: names,
		properties: Object.fromEntries(names.map((n) => [n, { type: 'string', minLength: 1 }]))
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
		stream: checked(form, 'stream'),
		request_schema: (jsonField(get('request_schema')) ?? {}) as EndpointRequestSchema,
		response_schema: jsonField(get('response_schema'))
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
	'/stream',
	'/request_schema',
	'/request_schema/path',
	'/request_schema/query',
	'/request_schema/headers',
	'/request_schema/body',
	'/response_schema'
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
	for (const part of REQUEST_PARTS) {
		const s = v.request_schema[part];
		if (s === undefined) continue;
		const p = schemaDocumentProblem(s, part);
		if (p) out[`/request_schema/${part}`] = p;
	}
	if (!out['/request_schema/path'] && !out['/path']) {
		const declared = Object.keys(
			(v.request_schema.path?.properties as Record<string, unknown> | undefined) ?? {}
		);
		const placeholders = pathParams(path);
		const missing = placeholders.filter((n) => !declared.includes(n));
		const extra = declared.filter((n) => !placeholders.includes(n));
		if (missing.length)
			out['/request_schema/path'] =
				`Declare the path parameter${missing.length > 1 ? 's' : ''} ${missing.map((n) => `{${n}}`).join(', ')} of the path.`;
		else if (extra.length)
			out['/request_schema/path'] =
				`${extra.join(', ')} ${extra.length > 1 ? 'are' : 'is'} not in the path (${path}).`;
	}
	if (v.response_schema) {
		const p = schemaDocumentProblem(v.response_schema, 'the response schema');
		if (p) out['/response_schema'] = p;
	}
	return out;
}

/** The request schema as the API takes it: only the parts that are there (`{}` = not sent). */
export function requestSchemaBody(rs: EndpointRequestSchema): EndpointRequestSchema {
	const out: EndpointRequestSchema = {};
	for (const part of REQUEST_PARTS) {
		const s = rs[part];
		if (s !== undefined && Object.keys(s).length) out[part] = s;
	}
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
		request_schema: requestSchemaBody(v.request_schema),
		reason
	};
	if (v.response_schema && Object.keys(v.response_schema).length)
		body.response_schema = v.response_schema;
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

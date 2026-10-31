import type { RequestState, SearchRequestsQuery } from '$lib/api/types';
import { pagingParams, parsePaging, type Paging } from '$lib/utils/paging';
import { lastRange, normalizeTime, timeChip, toLocalInput } from '$lib/utils/time';

// Shared with the DLQ filters since HUB-266; re-exported for the explorer page and its tests.
export { lastRange, normalizeTime, toLocalInput };

/**
 * Request explorer search <-> URL query (HUB-263). Names match `searchRequests` so the URL reads
 * like the API call; `status` and `code` are comma-separated (the contract's form/explode=false).
 * Times are RFC 3339 UTC. The contract requires the range to span at most 31 days.
 */
export const REQUEST_STATES: readonly RequestState[] = [
	'accepted',
	'in_flight',
	'succeeded',
	'retry_scheduled',
	'parked',
	'dead_lettered',
	'expired',
	'failed'
];

export const TEXT_KEYS = [
	'tenant_id',
	'integration_id',
	'vendor_id',
	'account_id',
	'idempotency_key'
] as const;
export const FILTER_KEYS = [...TEXT_KEYS, 'status', 'code', 'from', 'to'] as const;
export type FilterKey = (typeof FILTER_KEYS)[number];

export const filterLabels: Record<FilterKey, string> = {
	tenant_id: 'Tenant',
	integration_id: 'Integration',
	vendor_id: 'Vendor',
	account_id: 'Account',
	idempotency_key: 'Idempotency key',
	status: 'Status',
	code: 'Error code',
	from: 'From',
	to: 'To'
};

export interface Filters {
	tenant_id?: string;
	integration_id?: string;
	vendor_id?: string;
	account_id?: string;
	idempotency_key?: string;
	status?: RequestState[];
	code?: string[];
	/** RFC 3339, UTC, second precision: `2026-10-31T09:00:00Z`. */
	from?: string;
	to?: string;
}

export const MAX_RANGE_MS = 31 * 24 * 3_600_000;
const SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/;
const CODE = /^[A-Z][A-Z0-9_]{1,63}$/;

export interface SearchState {
	filters: Filters;
	paging: Paging;
	/** Why the time range cannot be searched (the API is not called), or null. */
	problem: string | null;
	query: SearchRequestsQuery;
}

/** Problem with the time range, or null. */
export function rangeProblem(from?: string, to?: string): string | null {
	if (!from || !to) return null;
	const span = Date.parse(to) - Date.parse(from);
	if (span <= 0) return '"To" must be later than "From".';
	if (span > MAX_RANGE_MS) return 'The time range can span at most 31 days.';
	return null;
}

function list(raw: string | null): string[] {
	return (raw ?? '')
		.split(',')
		.map((s) => s.trim())
		.filter(Boolean);
}

export function parseSearch(params: URLSearchParams): SearchState {
	const filters: Filters = {};
	for (const key of TEXT_KEYS) {
		const v = params.get(key)?.trim();
		if (!v) continue;
		if (key === 'idempotency_key' ? v.length <= 256 : SLUG.test(v)) filters[key] = v;
	}
	const status = list(params.get('status')).filter((s): s is RequestState =>
		(REQUEST_STATES as readonly string[]).includes(s)
	);
	if (status.length) filters.status = [...new Set(status)];
	const code = list(params.get('code')).filter((c) => CODE.test(c));
	if (code.length) filters.code = [...new Set(code)];
	const from = normalizeTime(params.get('from'));
	const to = normalizeTime(params.get('to'));
	if (from) filters.from = from;
	if (to) filters.to = to;

	const paging = parsePaging(params);
	const query: SearchRequestsQuery = { limit: paging.limit };
	for (const key of TEXT_KEYS) if (filters[key]) query[key] = filters[key];
	if (filters.status) query.status = filters.status;
	if (filters.code) query.code = filters.code;
	if (filters.from) query.from = filters.from;
	if (filters.to) query.to = filters.to;
	if (paging.cursor) query.cursor = paging.cursor;
	return { filters, paging, problem: rangeProblem(filters.from, filters.to), query };
}

/** `?...` for these filters and paging (stable order). */
export function searchFor(filters: Filters, paging?: Paging): string {
	const params = new URLSearchParams();
	for (const key of FILTER_KEYS) {
		const v = filters[key];
		if (Array.isArray(v) ? v.length : v) params.set(key, Array.isArray(v) ? v.join(',') : v!);
	}
	if (paging) for (const [k, v] of pagingParams(paging)) params.append(k, v);
	const s = params.toString();
	return s ? `?${s}` : '';
}

export function isFiltered(filters: Filters): boolean {
	return FILTER_KEYS.some((k) => {
		const v = filters[k];
		return Array.isArray(v) ? v.length > 0 : !!v;
	});
}

export function without(filters: Filters, key: FilterKey): Filters {
	const next = { ...filters };
	delete next[key];
	return next;
}

/** Chip text: `status=parked,dead_lettered` -> "Parked, Dead-lettered". */
export function chipValue(
	key: FilterKey,
	filters: Filters,
	stateLabel: (s: RequestState) => string
): string {
	if (key === 'status') return (filters.status ?? []).map(stateLabel).join(', ');
	if (key === 'code') return (filters.code ?? []).join(', ');
	if (key === 'from' || key === 'to') return timeChip(filters[key]!);
	return filters[key] ?? '';
}

/** The search form -> filters (empty fields dropped; times from `datetime-local`, read as UTC). */
export function filtersFromForm(form: FormData): { filters: Filters; limit: number | null } {
	const params = new URLSearchParams();
	for (const key of TEXT_KEYS) {
		const v = form.get(key);
		if (typeof v === 'string' && v.trim()) params.set(key, v.trim());
	}
	const status = form.getAll('status').filter((v): v is string => typeof v === 'string');
	if (status.length) params.set('status', status.join(','));
	const code = form.get('code');
	if (typeof code === 'string' && code.trim())
		params.set('code', list(code.toUpperCase().replace(/\s+/g, ',')).join(','));
	for (const key of ['from', 'to'] as const) {
		const v = normalizeTime(String(form.get(key) ?? ''));
		if (v) params.set(key, v);
	}
	const limit = Number(form.get('limit'));
	return {
		filters: parseSearch(params).filters,
		limit: Number.isFinite(limit) && limit > 0 ? limit : null
	};
}

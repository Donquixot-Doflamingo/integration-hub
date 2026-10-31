import type { ListIntegrationsQuery } from '$lib/api/types';
import { pagingParams, parsePaging, type Paging } from '$lib/utils/paging';

/**
 * Integrations list filters <-> URL query (HUB-256). Every filter lives in the URL so views are
 * shareable and survive reload; changing a filter drops the cursor (see `hrefFor`).
 * Names match the `listIntegrations` parameters so the URL reads like the API call.
 * TODO(CCR-002): an active-version filter needs `has_active_version` / `active_version_behind`
 * on the contract; filtering one page client-side would miss matches on later pages.
 */
export const FILTER_KEYS = [
	'q',
	'tenant_id',
	'capability',
	'vendor_id',
	'account_id',
	'paused'
] as const;
export type FilterKey = (typeof FILTER_KEYS)[number];

export const filterLabels: Record<FilterKey, string> = {
	q: 'Search',
	tenant_id: 'Owner tenant',
	capability: 'Capability',
	vendor_id: 'Vendor',
	account_id: 'Account',
	paused: 'State'
};

export type Filters = Partial<Record<FilterKey, string>>;

export interface ListState {
	filters: Filters;
	paging: Paging;
	/** What goes to `GET /integrations`. */
	query: ListIntegrationsQuery;
}

const MAX_LEN: Record<FilterKey, number> = {
	q: 128,
	tenant_id: 64,
	capability: 128,
	vendor_id: 64,
	account_id: 64,
	paused: 5
};

export function parseListState(params: URLSearchParams): ListState {
	const filters: Filters = {};
	for (const key of FILTER_KEYS) {
		const v = params.get(key)?.trim();
		if (!v || v.length > MAX_LEN[key]) continue;
		if (key === 'paused' && v !== 'true' && v !== 'false') continue;
		filters[key] = v;
	}
	const paging = parsePaging(params);
	const query: ListIntegrationsQuery = { limit: paging.limit };
	if (filters.q) query.q = filters.q;
	if (filters.tenant_id) query.tenant_id = filters.tenant_id;
	if (filters.capability) query.capability = filters.capability;
	if (filters.vendor_id) query.vendor_id = filters.vendor_id;
	if (filters.account_id) query.account_id = filters.account_id;
	if (filters.paused) query.paused = filters.paused === 'true';
	if (paging.cursor) query.cursor = paging.cursor;
	return { filters, paging, query };
}

/** `?...` for these filters and paging (filters first, in a stable order). */
export function searchFor(filters: Filters, paging?: Paging): string {
	const params = new URLSearchParams();
	for (const key of FILTER_KEYS) {
		const v = filters[key];
		if (v) params.set(key, v);
	}
	if (paging) for (const [k, v] of pagingParams(paging)) params.append(k, v);
	const s = params.toString();
	return s ? `?${s}` : '';
}

/** Readable value of a chip: `paused=true` -> "Paused only". */
export function chipValue(key: FilterKey, value: string): string {
	if (key === 'paused') return value === 'true' ? 'Paused only' : 'Live only';
	return value;
}

export function isFiltered(filters: Filters): boolean {
	return FILTER_KEYS.some((k) => !!filters[k]);
}

/** Filters without `key` (chip remove), keeping the page size but going back to the first page. */
export function without(filters: Filters, key: FilterKey): Filters {
	const next = { ...filters };
	delete next[key];
	return next;
}

/** Turns the filter form into filters, dropping empty fields. */
export function filtersFromForm(form: FormData): { filters: Filters; limit: number | null } {
	const filters: Filters = {};
	for (const key of FILTER_KEYS) {
		const v = form.get(key);
		if (typeof v === 'string' && v.trim()) filters[key] = v.trim();
	}
	const limit = Number(form.get('limit'));
	return { filters, limit: Number.isFinite(limit) && limit > 0 ? limit : null };
}

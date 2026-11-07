import type { ListIntegrationsQuery } from '$lib/api/types';
import { pagingParams, parsePaging, type Paging } from '$lib/utils/paging';

/**
 * Integrations list filters <-> URL query (HUB-256). Every filter lives in the URL so views are
 * shareable and survive reload; changing a filter drops the cursor (see `hrefFor`).
 * Names match the `listIntegrations` parameters so the URL reads like the API call.
 * The active-version filters (`has_active_version`, `active_version_behind`, hub-admin 1.1.0,
 * CCR-002 / HUB-283) are applied by the API before paging, so every page holds only matches.
 */
export const FILTER_KEYS = [
	'q',
	'tenant_id',
	'vendor_id',
	'account_id',
	'paused',
	'has_active_version',
	'active_version_behind'
] as const;
export type FilterKey = (typeof FILTER_KEYS)[number];

export const filterLabels: Record<FilterKey, string> = {
	q: 'Search',
	tenant_id: 'Owner tenant',
	vendor_id: 'Vendor',
	account_id: 'Account',
	paused: 'State',
	has_active_version: 'Active version',
	active_version_behind: 'Newer version or draft'
};

/** Filters that take `true` / `false` in the URL and the API. */
const BOOLEAN_KEYS: readonly FilterKey[] = [
	'paused',
	'has_active_version',
	'active_version_behind'
];

/** Option text per boolean filter value: the select shows these, the chips repeat them. */
export const booleanChoices: Record<
	'paused' | 'has_active_version' | 'active_version_behind',
	{ true: string; false: string }
> = {
	paused: { true: 'Paused only', false: 'Live only' },
	has_active_version: { true: 'Published', false: 'None (never published)' },
	active_version_behind: { true: 'Waiting', false: 'None waiting' }
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
	vendor_id: 64,
	account_id: 64,
	paused: 5,
	has_active_version: 5,
	active_version_behind: 5
};

export function parseListState(params: URLSearchParams): ListState {
	const filters: Filters = {};
	for (const key of FILTER_KEYS) {
		const v = params.get(key)?.trim();
		if (!v || v.length > MAX_LEN[key]) continue;
		if (BOOLEAN_KEYS.includes(key) && v !== 'true' && v !== 'false') continue;
		filters[key] = v;
	}
	const paging = parsePaging(params);
	const query: ListIntegrationsQuery = { limit: paging.limit };
	if (filters.q) query.q = filters.q;
	if (filters.tenant_id) query.tenant_id = filters.tenant_id;
	if (filters.vendor_id) query.vendor_id = filters.vendor_id;
	if (filters.account_id) query.account_id = filters.account_id;
	if (filters.paused) query.paused = filters.paused === 'true';
	if (filters.has_active_version) query.has_active_version = filters.has_active_version === 'true';
	if (filters.active_version_behind)
		query.active_version_behind = filters.active_version_behind === 'true';
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
	if (key === 'paused' || key === 'has_active_version' || key === 'active_version_behind') {
		return booleanChoices[key][value === 'true' ? 'true' : 'false'];
	}
	return value;
}

/**
 * Integrations without an active version match neither value of `active_version_behind`, so
 * `has_active_version=false` together with it always lists nothing. Says so, or null.
 */
export function conflictingFilters(filters: Filters): string | null {
	return filters.has_active_version === 'false' && filters.active_version_behind
		? 'Integrations without an active version have nothing newer to wait for, so "Active version: None" and "Newer version or draft" together match nothing. Remove one of them.'
		: null;
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

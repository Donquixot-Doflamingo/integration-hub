import type { ListTransformersQuery, TransformerKind } from '$lib/api/types';
import { pagingParams, parsePaging, type Paging } from '$lib/utils/paging';

/**
 * Transformers list filters <-> URL query (HUB-268). Names match `listTransformers`, so the URL
 * reads like the API call; changing a filter goes back to the first page.
 */
export const FILTER_KEYS = ['q', 'kind', 'vendor_id'] as const;
export type FilterKey = (typeof FILTER_KEYS)[number];
export type Filters = Partial<Record<FilterKey, string>>;

export const KINDS: readonly TransformerKind[] = ['request', 'response', 'inbound'];

export const kindLabels: Record<TransformerKind, string> = {
	request: 'Request (caller input to vendor request)',
	response: 'Response (vendor response to caller)',
	inbound: 'Inbound (vendor webhook)'
};

export const filterLabels: Record<FilterKey, string> = {
	q: 'Search',
	kind: 'Kind',
	vendor_id: 'Vendor'
};

const VALID: Record<FilterKey, (v: string) => boolean> = {
	q: (v) => v.length <= 128,
	kind: (v) => (KINDS as readonly string[]).includes(v),
	vendor_id: (v) => /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/.test(v)
};

export interface ListState {
	filters: Filters;
	paging: Paging;
	query: ListTransformersQuery;
}

export function parseListState(params: URLSearchParams): ListState {
	const filters: Filters = {};
	for (const key of FILTER_KEYS) {
		const v = params.get(key)?.trim();
		if (v && VALID[key](v)) filters[key] = v;
	}
	const paging = parsePaging(params);
	const query: ListTransformersQuery = { limit: paging.limit };
	if (filters.q) query.q = filters.q;
	if (filters.kind) query.kind = filters.kind as TransformerKind;
	if (filters.vendor_id) query.vendor_id = filters.vendor_id;
	if (paging.cursor) query.cursor = paging.cursor;
	return { filters, paging, query };
}

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

export function isFiltered(filters: Filters): boolean {
	return FILTER_KEYS.some((k) => !!filters[k]);
}

export function without(filters: Filters, key: FilterKey): Filters {
	const next = { ...filters };
	delete next[key];
	return next;
}

export function chipValue(key: FilterKey, value: string): string {
	return key === 'kind' ? (kindLabels[value as TransformerKind] ?? value) : value;
}

export function filtersFromForm(form: FormData): { filters: Filters; limit: number | null } {
	const params = new URLSearchParams();
	for (const key of FILTER_KEYS) {
		const v = form.get(key);
		if (typeof v === 'string' && v.trim()) params.set(key, v.trim());
	}
	const limit = Number(form.get('limit'));
	return {
		filters: parseListState(params).filters,
		limit: Number.isFinite(limit) && limit > 0 ? limit : null
	};
}

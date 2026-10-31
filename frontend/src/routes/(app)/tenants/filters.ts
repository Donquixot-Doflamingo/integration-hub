import type { ListTenantsQuery } from '$lib/api/types';
import { pagingParams, parsePaging, type Paging } from '$lib/utils/paging';

/**
 * Tenants list search <-> URL query (HUB-270). `q` is the contract's `Q` (substring of id and
 * display name, at most 128 characters); paging as everywhere else.
 */
export const Q_MAX = 128;

export interface ListState {
	q: string | undefined;
	paging: Paging;
	query: ListTenantsQuery;
}

export function parseListState(params: URLSearchParams): ListState {
	const raw = params.get('q')?.trim();
	const q = raw && raw.length <= Q_MAX ? raw : undefined;
	const paging = parsePaging(params);
	const query: ListTenantsQuery = { limit: paging.limit };
	if (q) query.q = q;
	if (paging.cursor) query.cursor = paging.cursor;
	return { q, paging, query };
}

export function searchFor(q: string | undefined, paging?: Paging): string {
	const params = new URLSearchParams();
	if (q) params.set('q', q);
	if (paging) for (const [k, v] of pagingParams(paging)) params.append(k, v);
	const s = params.toString();
	return s ? `?${s}` : '';
}

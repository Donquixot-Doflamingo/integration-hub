import { pagingParams, parsePaging, type Paging } from '$lib/utils/paging';

/**
 * A list search `q` plus cursor paging in the URL (vendors HUB-290, accounts HUB-291). `q` is the
 * contract's `Q` parameter (at most 128 characters).
 */
export const Q_MAX = 128;

export interface SearchState {
	q: string | undefined;
	paging: Paging;
	query: { q?: string; cursor?: string; limit: number };
}

export function parseSearchState(params: URLSearchParams): SearchState {
	const raw = params.get('q')?.trim();
	const q = raw && raw.length <= Q_MAX ? raw : undefined;
	const paging = parsePaging(params);
	const query: SearchState['query'] = { limit: paging.limit };
	if (q) query.q = q;
	if (paging.cursor) query.cursor = paging.cursor;
	return { q, paging, query };
}

/** `?q=...&cursor=...` for a page; `extra` adds other filters (kept before paging). */
export function searchFor(
	q: string | undefined,
	paging?: Paging,
	extra: Record<string, string | undefined> = {}
): string {
	const params = new URLSearchParams();
	if (q) params.set('q', q);
	for (const [k, v] of Object.entries(extra)) if (v) params.set(k, v);
	if (paging) for (const [k, v] of pagingParams(paging)) params.append(k, v);
	const s = params.toString();
	return s ? `?${s}` : '';
}

import type { AccountState, ListAccountsQuery } from '$lib/api/types';
import { parsePaging, pagingParams, type Paging } from '$lib/utils/paging';

/**
 * Accounts list filters <-> URL query (HUB-291): `vendor_id`, `state` and `q` as in
 * `listAccounts`, cursor paging as everywhere else.
 */
export const STATES: readonly AccountState[] = ['active', 'draining', 'retired'];
export const SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/;
export const Q_MAX = 128;

export interface Filters {
	q?: string;
	vendor_id?: string;
	state?: AccountState;
}

export interface ListState {
	filters: Filters;
	paging: Paging;
	query: ListAccountsQuery;
}

export function parseListState(params: URLSearchParams): ListState {
	const filters: Filters = {};
	const q = params.get('q')?.trim();
	if (q && q.length <= Q_MAX) filters.q = q;
	const vendor = params.get('vendor_id')?.trim();
	if (vendor && SLUG.test(vendor)) filters.vendor_id = vendor;
	const state = params.get('state');
	if ((STATES as readonly string[]).includes(state ?? '')) filters.state = state as AccountState;
	const paging = parsePaging(params);
	const query: ListAccountsQuery = { limit: paging.limit, ...filters };
	if (paging.cursor) query.cursor = paging.cursor;
	return { filters, paging, query };
}

export function searchFor(f: Filters, paging?: Paging): string {
	const params = new URLSearchParams();
	if (f.q) params.set('q', f.q);
	if (f.vendor_id) params.set('vendor_id', f.vendor_id);
	if (f.state) params.set('state', f.state);
	if (paging) for (const [k, v] of pagingParams(paging)) params.append(k, v);
	const s = params.toString();
	return s ? `?${s}` : '';
}

export const stateLabels: Record<AccountState, string> = {
	active: 'Active',
	draining: 'Draining',
	retired: 'Retired'
};

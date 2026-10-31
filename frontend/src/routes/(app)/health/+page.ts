import type { ApiResult } from '$lib/api/client';
import { consoleApi } from '$lib/api/console';
import type { HubApiError } from '$lib/api/errors';
import { collectPages } from '$lib/api/list';
import type { AccountSummary, Breaker } from '$lib/api/types';
import type { PageLoad } from './$types';
import { HEALTH_DEPENDENCY } from './board';

export interface BoardData {
	breakers: Breaker[];
	accounts: AccountSummary[];
	/** `listAccounts` failed: lifecycle states are missing, the board still shows breakers. */
	accountsError: HubApiError | null;
	/** When this answer arrived (ms since epoch), for "Updated 5 s ago". */
	fetchedAt: number;
}

// Health board (HUB-261): every breaker (all pages) plus account lifecycle states (all pages).
// Re-run by `invalidate(HEALTH_DEPENDENCY)` on each auto-refresh tick.
export const load: PageLoad = (event) => {
	event.depends(HEALTH_DEPENDENCY);
	const api = consoleApi(event);
	const breakers = collectPages((cursor) =>
		api.call((c) =>
			c.GET('/breakers', { params: { query: { limit: 200, ...(cursor ? { cursor } : {}) } } })
		)
	);
	const accounts = collectPages((cursor) =>
		api.call((c) =>
			c.GET('/accounts', { params: { query: { limit: 200, ...(cursor ? { cursor } : {}) } } })
		)
	);
	const board: Promise<ApiResult<BoardData>> = Promise.all([breakers, accounts]).then(([b, a]) => {
		if (!b.ok) return b;
		return {
			...b,
			data: {
				breakers: b.data.items,
				accounts: a.ok ? a.data.items : [],
				accountsError: a.ok ? null : a.error,
				fetchedAt: Date.now()
			}
		};
	});
	return { board };
};

import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';
import { parseListState } from './filters';

// Accounts list (HUB-291): listAccounts (viewer) with vendor, state, search and cursor from the
// URL. Tenant viewers are below `viewer` and never see accounts: no call is made for them.
export const load: PageLoad = async (event) => {
	const state = parseListState(event.url.searchParams);
	const { access } = await event.parent();
	return {
		state,
		list: access.tenantScoped
			? null
			: consoleApi(event).call((c) => c.GET('/accounts', { params: { query: state.query } }))
	};
};

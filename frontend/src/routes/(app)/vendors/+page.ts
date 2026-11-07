import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';
import { parseSearchState } from './filters';

// Vendors list (HUB-290): listVendors (viewer) with the search and cursor from the URL. Tenant
// viewers are below `viewer` and never see vendors, so no call is made for them.
export const load: PageLoad = async (event) => {
	const state = parseSearchState(event.url.searchParams);
	const { access } = await event.parent();
	return {
		state,
		list: access.tenantScoped
			? null
			: consoleApi(event).call((c) => c.GET('/vendors', { params: { query: state.query } }))
	};
};

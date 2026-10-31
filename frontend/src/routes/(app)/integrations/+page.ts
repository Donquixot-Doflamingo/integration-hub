import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';
import { parseListState } from './filters';

// listIntegrations with the filters and cursor from the URL (HUB-256). Not awaited, so the page
// keeps the previous rows on screen while the next page or filter result loads.
export const load: PageLoad = (event) => {
	const state = parseListState(event.url.searchParams);
	return {
		state,
		list: consoleApi(event).call((c) => c.GET('/integrations', { params: { query: state.query } }))
	};
};

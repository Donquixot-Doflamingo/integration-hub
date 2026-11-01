import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';
import { parseListState } from './filters';

// Change log (HUB-274): listChangeLog with the filters and cursor from the URL. A reversed time
// range is not sent; the page says why.
export const load: PageLoad = (event) => {
	const state = parseListState(event.url.searchParams);
	return {
		state,
		list: state.problem
			? null
			: consoleApi(event).call((c) => c.GET('/change-log', { params: { query: state.query } }))
	};
};

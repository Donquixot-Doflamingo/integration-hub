import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';
import { parseSearch } from './filters';

// Request explorer (HUB-263): searchRequests with the filters and cursor from the URL. A time
// range the contract would reject (over 31 days, or reversed) is not sent; the page says why.
export const load: PageLoad = (event) => {
	const state = parseSearch(event.url.searchParams);
	return {
		state,
		list: state.problem
			? null
			: consoleApi(event).call((c) =>
					c.GET('/requests', {
						params: { query: state.query },
						// Contract: `status` and `code` are style form, explode false (`status=a,b`).
						querySerializer: { array: { style: 'form', explode: false } }
					})
				)
	};
};

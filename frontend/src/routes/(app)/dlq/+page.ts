import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';
import { parseListState } from './filters';

// DLQ list (HUB-266): listDeadLetters with the filters and cursor from the URL. A reversed time
// range is not sent; the page says why. Not awaited, so the previous rows stay while loading.
export const load: PageLoad = (event) => {
	const state = parseListState(event.url.searchParams);
	return {
		state,
		list: state.problem
			? null
			: consoleApi(event).call((c) => c.GET('/dlq', { params: { query: state.query } }))
	};
};

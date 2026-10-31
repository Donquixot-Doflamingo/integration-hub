import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';
import { parseListState } from './filters';

// Transformers list (HUB-268): listTransformers with the filters and cursor from the URL.
export const load: PageLoad = (event) => {
	const state = parseListState(event.url.searchParams);
	return {
		state,
		list: consoleApi(event).call((c) => c.GET('/transformers', { params: { query: state.query } }))
	};
};

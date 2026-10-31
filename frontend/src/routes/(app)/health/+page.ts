import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';

// listBreakers (the health board, HUB-261, builds on this list).
export const load: PageLoad = (event) => ({
	list: consoleApi(event).call((c) => c.GET('/breakers'))
});

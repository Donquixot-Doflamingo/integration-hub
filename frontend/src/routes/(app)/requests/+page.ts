import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';

// searchRequests without filters: the most recent requests (filters arrive with the explorer ticket).
export const load: PageLoad = (event) => ({
	list: consoleApi(event).call((c) => c.GET('/requests'))
});

import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';

// listChangeLog.
export const load: PageLoad = (event) => ({
	list: consoleApi(event).call((c) => c.GET('/change-log'))
});

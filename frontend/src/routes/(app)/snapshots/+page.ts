import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';

// listSnapshots.
export const load: PageLoad = (event) => ({
	list: consoleApi(event).call((c) => c.GET('/snapshots'))
});

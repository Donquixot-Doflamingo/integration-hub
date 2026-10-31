import { consoleApi } from '$lib/api/console';
import { parsePaging } from '$lib/utils/paging';
import type { PageLoad } from './$types';

// Snapshots (HUB-273): listSnapshots (newest first) with the cursor from the URL, and
// getLatestSnapshot (no long-poll: wait 0) to mark the snapshot gateway nodes load now.
export const load: PageLoad = (event) => {
	const paging = parsePaging(event.url.searchParams);
	const api = consoleApi(event);
	return {
		paging,
		list: api.call((c) =>
			c.GET('/snapshots', {
				params: {
					query: { limit: paging.limit, ...(paging.cursor ? { cursor: paging.cursor } : {}) }
				}
			})
		),
		latest: api.call((c) => c.GET('/snapshots/latest', { params: { query: { wait: 0 } } }))
	};
};

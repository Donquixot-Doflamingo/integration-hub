import { redirect } from '@sveltejs/kit';
import { consoleApi } from '$lib/api/console';
import { collectPages } from '$lib/api/list';
import type { PageLoad } from './$types';
import { compareSearch, defaultPair, parseCompare } from './compare';

// diffIntegrationVersions for `?from=&to=` (HUB-258), plus the version list for the pickers.
// Without a pair in the URL, pick a sensible default and redirect, so the URL is always shareable.
export const load: PageLoad = async (event) => {
	const api = consoleApi(event);
	const id = event.params.integration_id;
	const path = { integration_id: id };
	const { from, to, view } = parseCompare(event.url.searchParams);
	const versions = collectPages((cursor) =>
		api.call((c) =>
			c.GET('/integrations/{integration_id}/versions', {
				params: { path, query: { limit: 200, ...(cursor ? { cursor } : {}) } }
			})
		)
	);

	if (from === null || to === null) {
		const list = await versions;
		const pair = list.ok ? defaultPair(list.data.items) : null;
		if (pair) {
			redirect(307, `${event.url.pathname}${compareSearch(pair.from, pair.to, view)}`);
		}
	}

	return {
		integrationId: id,
		from,
		to,
		view,
		versions,
		diff:
			from !== null && to !== null && from !== to
				? api.call((c) =>
						c.GET('/integrations/{integration_id}/diff', { params: { path, query: { from, to } } })
					)
				: null
	};
};

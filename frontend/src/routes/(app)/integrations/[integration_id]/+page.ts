import { consoleApi } from '$lib/api/console';
import { collectPages } from '$lib/api/list';
import type { PageLoad } from './$types';

// getIntegration (header + active spec) and every version (listIntegrationVersions, all pages,
// newest first). Both stream, so the header shows as soon as it answers (HUB-257).
export const load: PageLoad = (event) => {
	const api = consoleApi(event);
	const path = { integration_id: event.params.integration_id };
	return {
		integrationId: event.params.integration_id,
		integration: api.call((c) => c.GET('/integrations/{integration_id}', { params: { path } })),
		versions: collectPages((cursor) =>
			api.call((c) =>
				c.GET('/integrations/{integration_id}/versions', {
					params: { path, query: { limit: 200, ...(cursor ? { cursor } : {}) } }
				})
			)
		).then((r) =>
			// Contract: newest first. Sort anyway so the history never depends on server order.
			r.ok
				? {
						...r,
						data: { ...r.data, items: [...r.data.items].sort((a, b) => b.version - a.version) }
					}
				: r
		)
	};
};

import { consoleApi } from '$lib/api/console';
import { collectPages } from '$lib/api/list';
import type { PageLoad } from './$types';

// Transformer (HUB-268): getTransformer (header + latest version in full) and every version
// (listTransformerVersions, all pages, newest first; YAML and fixtures omitted there).
export const load: PageLoad = (event) => {
	const api = consoleApi(event);
	const path = { transformer_id: event.params.transformer_id };
	return {
		transformerId: event.params.transformer_id,
		transformer: api.call((c) => c.GET('/transformers/{transformer_id}', { params: { path } })),
		versions: collectPages((cursor) =>
			api.call((c) =>
				c.GET('/transformers/{transformer_id}/versions', {
					params: { path, query: { limit: 200, ...(cursor ? { cursor } : {}) } }
				})
			)
		).then((r) =>
			r.ok
				? {
						...r,
						data: { ...r.data, items: [...r.data.items].sort((a, b) => b.version - a.version) }
					}
				: r
		)
	};
};

import { consoleApi } from '$lib/api/console';
import { collectPages } from '$lib/api/list';
import type { PageLoad } from './$types';

// Create an integration (HUB-292): tenants (owner, grants) and capabilities for the pickers, as
// one result (either failing shows its error with a retry).
export const load: PageLoad = (event) => {
	const api = consoleApi(event);
	const all = { limit: 200 } as const;
	const tenants = collectPages((cursor) =>
		api.call((c) => c.GET('/tenants', { params: { query: { ...all, cursor } } }))
	);
	const capabilities = collectPages((cursor) =>
		api.call((c) => c.GET('/capabilities', { params: { query: { ...all, cursor } } }))
	);
	return {
		lookups: Promise.all([tenants, capabilities]).then(([t, c]) => {
			if (!t.ok) return t;
			if (!c.ok) return c;
			return { ...t, data: { tenants: t.data.items, capabilities: c.data.items } };
		})
	};
};

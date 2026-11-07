import { consoleApi } from '$lib/api/console';
import { collectPages } from '$lib/api/list';
import type { PageLoad } from './$types';

// Create an integration (HUB-292, HUB-436): tenants for the owner and grants pickers.
export const load: PageLoad = (event) => {
	const api = consoleApi(event);
	const tenants = collectPages((cursor) =>
		api.call((c) => c.GET('/tenants', { params: { query: { limit: 200, cursor } } }))
	);
	return {
		lookups: tenants.then((t) => (t.ok ? { ...t, data: { tenants: t.data.items } } : t))
	};
};

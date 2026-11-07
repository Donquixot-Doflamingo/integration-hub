import { consoleApi } from '$lib/api/console';
import { collectPages } from '$lib/api/list';
import type { PageLoad } from './$types';

// Create an account (HUB-291): every vendor (listVendors, all pages) for the vendor and region
// pickers; `?vendor_id=` preselects one (from a vendor's page or a filtered list).
export const load: PageLoad = (event) => {
	const api = consoleApi(event);
	return {
		vendorId: event.url.searchParams.get('vendor_id') ?? '',
		vendors: collectPages((cursor) =>
			api.call((c) =>
				c.GET('/vendors', { params: { query: { limit: 200, ...(cursor ? { cursor } : {}) } } })
			)
		)
	};
};

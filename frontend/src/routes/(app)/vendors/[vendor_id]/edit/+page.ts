import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';

// Edit a vendor (HUB-290): getVendor for the current definition and its ETag (sent as If-Match).
export const load: PageLoad = (event) => {
	const vendorId = event.params.vendor_id;
	return {
		vendorId,
		vendor: consoleApi(event)
			.call((c) => c.GET('/vendors/{vendor_id}', { params: { path: { vendor_id: vendorId } } }))
			.then((r) => (r.ok ? { ...r, data: { vendor: r.data, etag: r.etag } } : r))
	};
};

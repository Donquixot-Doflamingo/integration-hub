import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';

// Vendor detail (HUB-290): getVendor (viewer) with every endpoint version. The ETag travels with
// the result so the edit page can send it back as If-Match. Not loaded for tenant viewers.
export const load: PageLoad = async (event) => {
	const vendorId = event.params.vendor_id;
	const { access } = await event.parent();
	return {
		vendorId,
		vendor: access.tenantScoped
			? null
			: consoleApi(event).call((c) =>
					c.GET('/vendors/{vendor_id}', { params: { path: { vendor_id: vendorId } } })
				)
	};
};

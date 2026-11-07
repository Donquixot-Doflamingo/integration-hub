import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';
import { latestByName } from '../../../endpoint-form';

// One endpoint version (HUB-290): getVendorEndpoint, plus getVendor to know the latest version of
// its name (only the latest offers "New version"; older ones link to it). If getVendor fails the
// endpoint still shows, without the new-version control.
export const load: PageLoad = async (event) => {
	const { vendor_id, endpoint_ref } = event.params;
	const { access } = await event.parent();
	if (access.tenantScoped)
		return { vendorId: vendor_id, endpointRef: endpoint_ref, endpoint: null };
	const api = consoleApi(event);
	const vendor = api.call((c) =>
		c.GET('/vendors/{vendor_id}', { params: { path: { vendor_id } } })
	);
	const endpoint = api
		.call((c) =>
			c.GET('/vendors/{vendor_id}/endpoints/{endpoint_ref}', {
				params: { path: { vendor_id, endpoint_ref } }
			})
		)
		.then(async (r) => {
			if (!r.ok) return r;
			const v = await vendor;
			const latest = v.ok ? (latestByName(v.data.endpoints).get(r.data.name) ?? null) : null;
			return { ...r, data: { endpoint: r.data, latest } };
		});
	return { vendorId: vendor_id, endpointRef: endpoint_ref, endpoint };
};

import type { ApiResult } from '$lib/api/client';
import type { AdminClient } from '$lib/api/client';
import { collectPages } from '$lib/api/list';
import type { VendorEndpoint } from '$lib/api/types';

/** Suggestions for the transformer authoring form (HUB-293, HUB-436): vendor endpoint versions. */
export interface AuthoringLookups {
	/** `<vendor_id>.<endpoint>@<n>` for every endpoint version of every vendor (first 25 vendors). */
	endpointRefs: string[];
	/** The endpoint version behind each ref (its request schema prefills request samples). */
	endpoints: Record<string, VendorEndpoint>;
}

const MAX_VENDORS = 25;

export async function authoringLookups(api: AdminClient): Promise<ApiResult<AuthoringLookups>> {
	const vendors = await collectPages((cursor) =>
		api.call((c) => c.GET('/vendors', { params: { query: { limit: 200, cursor } } }))
	);
	if (!vendors.ok) return vendors;
	const details = await Promise.all(
		vendors.data.items
			.slice(0, MAX_VENDORS)
			.map((v) =>
				api.call((c) =>
					c.GET('/vendors/{vendor_id}', { params: { path: { vendor_id: v.vendor_id } } })
				)
			)
	);
	const endpoints: Record<string, VendorEndpoint> = {};
	for (const d of details) {
		if (!d.ok) continue;
		for (const e of d.data.endpoints) endpoints[`${d.data.vendor_id}.${e.endpoint_ref}`] = e;
	}
	return { ...vendors, data: { endpointRefs: Object.keys(endpoints), endpoints } };
}

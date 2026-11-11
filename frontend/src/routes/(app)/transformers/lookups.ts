import type { ApiResult } from '$lib/api/client';
import type { AdminClient } from '$lib/api/client';
import { collectPages } from '$lib/api/list';
import type { VendorEndpoint } from '$lib/api/types';

/** Suggestions for the transformer authoring form (HUB-293, HUB-436): vendor endpoint versions. */
export interface AuthoringLookups {
	/** `<vendor_id>.<endpoint>@<n>` for every endpoint version of every vendor. */
	endpointRefs: string[];
	/** The endpoint version behind each ref (its request schema prefills request samples). */
	endpoints: Record<string, VendorEndpoint>;
	/** Vendors whose endpoints could not be read, so their suggestions are missing (HUB-455). */
	problems: string[];
}

/** Vendor details read at once: all vendors are read, but not all in the same instant. */
const DETAIL_CONCURRENCY = 8;

export async function authoringLookups(api: AdminClient): Promise<ApiResult<AuthoringLookups>> {
	// Every page of vendors (HUB-455: it used to stop at the first 25).
	const vendors = await collectPages((cursor) =>
		api.call((c) => c.GET('/vendors', { params: { query: { limit: 200, cursor } } }))
	);
	if (!vendors.ok) return vendors;
	const ids = vendors.data.items.map((v) => v.vendor_id);
	const details: Awaited<ReturnType<typeof readVendor>>[] = new Array(ids.length);
	let next = 0;
	const readVendor = (id: string) =>
		api.call((c) => c.GET('/vendors/{vendor_id}', { params: { path: { vendor_id: id } } }));
	await Promise.all(
		Array.from({ length: Math.min(DETAIL_CONCURRENCY, ids.length) }, async () => {
			while (next < ids.length) {
				const i = next++;
				details[i] = await readVendor(ids[i]!);
			}
		})
	);
	const endpoints: Record<string, VendorEndpoint> = {};
	const problems: string[] = [];
	details.forEach((d, i) => {
		if (!d.ok) {
			problems.push(`Could not read the endpoints of ${ids[i]}: ${d.error.message}`);
			return;
		}
		for (const e of d.data.endpoints) endpoints[`${d.data.vendor_id}.${e.endpoint_ref}`] = e;
	});
	return { ...vendors, data: { endpointRefs: Object.keys(endpoints), endpoints, problems } };
}

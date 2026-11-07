import type { ApiResult } from '$lib/api/client';
import type { AdminClient } from '$lib/api/client';
import { collectPages } from '$lib/api/list';
import type { CapabilitySummary } from '$lib/api/types';

/** Suggestions for the transformer authoring form (HUB-293): capabilities and vendor endpoints. */
export interface AuthoringLookups {
	capabilities: CapabilitySummary[];
	/** `<vendor_id>.<endpoint>@<n>` for every endpoint version of every vendor (first 25 vendors). */
	endpointRefs: string[];
}

const MAX_VENDORS = 25;

export async function authoringLookups(api: AdminClient): Promise<ApiResult<AuthoringLookups>> {
	const all = { limit: 200 } as const;
	const [caps, vendors] = await Promise.all([
		collectPages((cursor) =>
			api.call((c) => c.GET('/capabilities', { params: { query: { ...all, cursor } } }))
		),
		collectPages((cursor) =>
			api.call((c) => c.GET('/vendors', { params: { query: { ...all, cursor } } }))
		)
	]);
	if (!caps.ok) return caps;
	const details = vendors.ok
		? await Promise.all(
				vendors.data.items
					.slice(0, MAX_VENDORS)
					.map((v) =>
						api.call((c) =>
							c.GET('/vendors/{vendor_id}', { params: { path: { vendor_id: v.vendor_id } } })
						)
					)
			)
		: [];
	const endpointRefs = details.flatMap((d) =>
		d.ok ? d.data.endpoints.map((e) => `${d.data.vendor_id}.${e.endpoint_ref}`) : []
	);
	return { ...caps, data: { capabilities: caps.data.items, endpointRefs } };
}

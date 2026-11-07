import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';
import { latestByName } from '../../../endpoint-form';

// Add an endpoint or the next version of one (HUB-290, HUB-436). `?from=<name>` starts from the
// latest version of that name (versions are immutable, so "editing" creates <name>@<n+1>).
export const load: PageLoad = async (event) => {
	const vendor_id = event.params.vendor_id;
	const from = event.url.searchParams.get('from');
	const api = consoleApi(event);
	const vendor = api
		.call((c) => c.GET('/vendors/{vendor_id}', { params: { path: { vendor_id } } }))
		.then((r) => {
			if (!r.ok) return r;
			const latest = from ? (latestByName(r.data.endpoints).get(from) ?? null) : null;
			return { ...r, data: { vendor: r.data, from: latest } };
		});
	return { vendorId: vendor_id, fromName: from, start: vendor };
};

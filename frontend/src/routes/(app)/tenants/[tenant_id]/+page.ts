import { error } from '@sveltejs/kit';
import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';

// Tenant detail (HUB-270): getTenant (tenant_viewer). The ETag travels with the result so edits
// send it back as If-Match. A tenant viewer asking for a tenant outside their scope gets the same
// 404 the API would answer, without the call.
export const load: PageLoad = async (event) => {
	const tenantId = event.params.tenant_id;
	const { access } = await event.parent();
	if (access.tenantScoped && !access.tenants.includes(tenantId)) {
		error(404, { message: `Tenant ${tenantId} is not one of your tenants.`, code: 'NOT_FOUND' });
	}
	const api = consoleApi(event);
	return {
		tenantId,
		tenant: api.call((c) =>
			c.GET('/tenants/{tenant_id}', { params: { path: { tenant_id: tenantId } } })
		)
	};
};

import { error } from '@sveltejs/kit';
import { consoleApi } from '$lib/api/console';
import { collectPages } from '$lib/api/list';
import type { ApiKeyState } from '$lib/api/types';
import { STATES } from './keys';
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
	// HUB-271: the tenant's API keys (listApiKeys, tenant_viewer), all pages; `key_state` filters.
	const raw = event.url.searchParams.get('key_state');
	const keyState = (STATES as readonly string[]).includes(raw ?? '')
		? (raw as ApiKeyState)
		: undefined;
	return {
		tenantId,
		keyState,
		keys: collectPages((cursor) =>
			api.call((c) =>
				c.GET('/api-keys', {
					params: {
						query: {
							tenant_id: tenantId,
							limit: 200,
							...(keyState ? { state: keyState } : {}),
							...(cursor ? { cursor } : {})
						}
					}
				})
			)
		),
		tenant: api.call((c) =>
			c.GET('/tenants/{tenant_id}', { params: { path: { tenant_id: tenantId } } })
		)
	};
};

import { resolve } from '$app/paths';
import type { ObjectType } from '$lib/api/types';

/**
 * The console page of a registry object named by the change log (HUB-274) or a snapshot's changed
 * objects (HUB-273), when there is one. API keys and callback
 * endpoints live on their tenant's page, which the entry does not name, so they get no link.
 */
export function objectHref(type: ObjectType, id: string): string | null {
	switch (type) {
		case 'integration':
			return resolve('/(app)/integrations/[integration_id]', { integration_id: id });
		case 'transformer':
			return resolve('/(app)/transformers/[transformer_id]', { transformer_id: id });
		case 'tenant':
			return resolve('/(app)/tenants/[tenant_id]', { tenant_id: id });
		case 'request':
			return resolve('/(app)/requests/[request_id]', { request_id: id });
		default:
			return null;
	}
}

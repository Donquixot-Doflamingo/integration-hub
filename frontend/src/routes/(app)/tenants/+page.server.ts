import { actionApi, finish, invalid, readReason, requireSession } from '$lib/server/actions';
import type { Actions } from './$types';
import { API_FIELDS, createBody, valuesFromForm } from './tenant-form';

// HUB-270: create a tenant (createTenant, admin). Checked against TenantCreate before calling;
// API VALIDATION_FAILED pointers land on their field; 409 CONFLICT when the id is taken. In ISFC
// mode the API answers 409 `tenant_managed_by_isfc` and the console hides the control (HUB-485).
export const actions = {
	create: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('create', form);
		if (!reason.ok) return reason.failure;
		const built = createBody(valuesFromForm(form), reason.reason);
		if (!built.ok) return invalid('create', built.fields);
		const res = await actionApi(event).call((c) => c.POST('/tenants', { body: built.body }));
		return finish('create', res, API_FIELDS);
	},

	// HUB-485: "Sync now" (syncTenants, admin) runs the ISFC reconciler once for every tenant.
	// A sync that could not reach ISFC is still a 200 (`result: failed`): the page shows it as a warning.
	syncAll: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('syncAll', form);
		if (!reason.ok) return reason.failure;
		const res = await actionApi(event).call((c) =>
			c.POST('/tenants/sync', { body: { reason: reason.reason } })
		);
		return finish('syncAll', res);
	}
} satisfies Actions;

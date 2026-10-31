import { actionApi, finish, invalid, readReason, requireSession } from '$lib/server/actions';
import type { Actions } from './$types';
import { API_FIELDS, createBody, valuesFromForm } from './tenant-form';

// HUB-270: create a tenant (createTenant, admin). Checked against TenantCreate before calling;
// API VALIDATION_FAILED pointers land on their field; 409 CONFLICT when the id is taken.
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
	}
} satisfies Actions;

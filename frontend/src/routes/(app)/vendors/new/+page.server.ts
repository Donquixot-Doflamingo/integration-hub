import {
	actionApi,
	finishByPointer,
	invalid,
	readReason,
	requireSession
} from '$lib/server/actions';
import type { Actions } from './$types';
import { createBody, POINTERS, valuesFromForm } from '../vendor-form';

// HUB-290: create a vendor (createVendor, admin). Checked against VendorCreate before calling; API
// VALIDATION_FAILED pointers land on their field; 409 CONFLICT when the id is taken.
export const actions = {
	create: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('create', form);
		if (!reason.ok) return reason.failure;
		const built = createBody(valuesFromForm(form), reason.reason);
		if (!built.ok) return invalid('create', built.fields);
		const res = await actionApi(event).call((c) => c.POST('/vendors', { body: built.body }));
		return finishByPointer('create', res, POINTERS);
	}
} satisfies Actions;

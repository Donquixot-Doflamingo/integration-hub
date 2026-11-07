import {
	actionApi,
	finishByPointer,
	invalid,
	readReason,
	requireSession
} from '$lib/server/actions';
import type { Actions } from './$types';
import { createBody, POINTERS, valuesFromForm } from './integration-form';

// HUB-292: create an integration (createIntegration, editor). It has no version yet; the next step
// is the first draft on its publish page.
export const actions = {
	create: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('create', form);
		if (!reason.ok) return reason.failure;
		const built = createBody(valuesFromForm(form), reason.reason);
		if (!built.ok) return invalid('create', built.fields);
		const res = await actionApi(event).call((c) => c.POST('/integrations', { body: built.body }));
		return finishByPointer('create', res, POINTERS);
	}
} satisfies Actions;

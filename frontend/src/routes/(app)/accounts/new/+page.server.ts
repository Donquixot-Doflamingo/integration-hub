import {
	actionApi,
	finishByPointer,
	invalid,
	readReason,
	CREDENTIALS_NEED_JS,
	requireEnhanced,
	requireSession
} from '$lib/server/actions';
import type { Actions } from './$types';
import { createBody, CREATE_POINTERS, secretValues, valuesFromForm } from '../account-form';

// HUB-291: create an account with its first credentials (createAccount, admin). The credential
// values only pass through: they go to the admin API and nowhere else (never logged, never in the
// answer; any error text that echoes one is scrubbed). Enhanced submissions only, so the secrets
// never sit in a plain form post the browser may offer to resend.
export const actions = {
	create: async (event) => {
		requireSession(event);
		const notEnhanced = requireEnhanced('create', event, CREDENTIALS_NEED_JS);
		if (notEnhanced) return notEnhanced;
		const form = await event.request.formData();
		const values = valuesFromForm(form);
		const secrets = secretValues(values.credentials);
		const reason = readReason('create', form);
		if (!reason.ok) return reason.failure;
		const built = createBody(values, reason.reason);
		if (!built.ok) return invalid('create', built.fields);
		const res = await actionApi(event).call((c) => c.POST('/accounts', { body: built.body }));
		return finishByPointer('create', res, CREATE_POINTERS, secrets);
	}
} satisfies Actions;

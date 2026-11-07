import type { AccountState } from '$lib/api/types';
import {
	actionApi,
	CREDENTIALS_NEED_JS,
	finish,
	finishByPointer,
	int,
	invalid,
	readReason,
	requireEnhanced,
	requireSession,
	text
} from '$lib/server/actions';
import type { Actions } from './$types';
import {
	credentialProblem,
	credentialsFromForm,
	EDIT_POINTERS,
	expiryProblem,
	secretValues,
	updateBody,
	type EditValues
} from '../account-form';
import { normalizeTime } from '$lib/utils/time';

/** `EndpointRefValue`: `<name>@<n>`. */
const ENDPOINT_REF = /^[a-z][a-z0-9_]{0,63}@[1-9][0-9]*$/;
const STATES: readonly AccountState[] = ['active', 'draining', 'retired'];

function editValues(form: FormData, prefix = ''): EditValues {
	return {
		config: text(form, `${prefix}config`),
		rps: text(form, `${prefix}rps`),
		daily: text(form, `${prefix}daily`)
	};
}

// HUB-291: account mutations. Every one runs on the server (the admin token never reaches the
// browser) and needs a reason, except the connection test, which the contract makes
// non-mutating (no reason, no change id; audited by the Hub).
export const actions = {
	/** updateAccount (admin): merge-patch of config and quota, with the loaded ETag as If-Match. */
	update: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('update', form);
		if (!reason.ok) return reason.failure;
		const built = updateBody(editValues(form), editValues(form, 'before.'), reason.reason);
		if (!built.ok) return invalid('update', built.fields);
		const etag = text(form, 'if_match').trim();
		const res = await actionApi(event).call((c) =>
			c.PATCH('/accounts/{account_id}', {
				params: {
					path: { account_id: event.params.account_id },
					header: etag ? { 'If-Match': etag } : {}
				},
				body: built.body
			})
		);
		return finishByPointer('update', res, EDIT_POINTERS);
	},

	/** testAccountConnection (editor): the vendor's probe with the active or next credential. */
	testConnection: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const slot = text(form, 'slot');
		if (slot !== 'active' && slot !== 'next')
			return invalid('testConnection', { slot: 'Pick the active or the next credential.' });
		const ref = text(form, 'endpoint_ref').trim();
		if (ref && !ENDPOINT_REF.test(ref))
			return invalid('testConnection', { endpoint_ref: 'Pick one of the vendor endpoints.' });
		const res = await actionApi(event).call((c) =>
			c.POST('/accounts/{account_id}/test-connection', {
				params: { path: { account_id: event.params.account_id } },
				body: { slot, ...(ref ? { endpoint_ref: ref } : {}) }
			})
		);
		return finish('testConnection', res, ['slot', 'endpoint_ref']);
	},

	/**
	 * addNextCredential (admin), rotation step 1. The secret values only pass through to the API;
	 * enhanced submissions only; any error text echoing one is scrubbed.
	 */
	addNext: async (event) => {
		requireSession(event);
		const notEnhanced = requireEnhanced('addNext', event, CREDENTIALS_NEED_JS);
		if (notEnhanced) return notEnhanced;
		const form = await event.request.formData();
		const rows = credentialsFromForm(form);
		const secrets = secretValues(rows);
		const reason = readReason('addNext', form);
		if (!reason.ok) return reason.failure;
		const problem = credentialProblem(rows);
		const expires = text(form, 'expires_at');
		const expiry = expiryProblem(expires);
		if (problem || expiry)
			return invalid('addNext', {
				...(problem ? { '/credentials': problem } : {}),
				...(expiry ? { '/expires_at': expiry } : {})
			});
		const credentials: Record<string, string> = {};
		for (const r of rows) if (r.key.trim()) credentials[r.key.trim()] = r.value;
		const res = await actionApi(event).call((c) =>
			c.POST('/accounts/{account_id}/credentials/next', {
				params: { path: { account_id: event.params.account_id } },
				body: {
					credentials,
					expires_at: expires.trim() ? normalizeTime(expires) : null,
					reason: reason.reason
				}
			})
		);
		return finishByPointer('addNext', res, ['/credentials', '/expires_at'], secrets);
	},

	/** promoteNextCredential (admin), rotation step 2, guarded by `expected_next_version`. */
	promote: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('promote', form);
		if (!reason.ok) return reason.failure;
		const expected = int(form, 'expected_next_version');
		if (!expected || expected < 1)
			return invalid('promote', { expected_next_version: 'Reload the page and try again.' });
		const res = await actionApi(event).call((c) =>
			c.POST('/accounts/{account_id}/credentials/promote', {
				params: { path: { account_id: event.params.account_id } },
				body: { expected_next_version: expected, reason: reason.reason }
			})
		);
		return finish('promote', res);
	},

	/** deleteOldCredential (admin), rotation step 3: destroys the previous slot (id typed). */
	deleteOld: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('deleteOld', form);
		if (!reason.ok) return reason.failure;
		const expected = int(form, 'expected_previous_version');
		if (!expected || expected < 1)
			return invalid('deleteOld', { expected_previous_version: 'Reload the page and try again.' });
		if (text(form, 'confirm').trim() !== event.params.account_id)
			return invalid('deleteOld', { confirm: `Type ${event.params.account_id} to confirm.` });
		const res = await actionApi(event).call((c) =>
			c.POST('/accounts/{account_id}/credentials/delete-old', {
				params: { path: { account_id: event.params.account_id } },
				body: { expected_previous_version: expected, reason: reason.reason }
			})
		);
		return finish('deleteOld', res);
	},

	/** transitionAccountLifecycle (admin): active <-> draining -> retired (retire: id typed). */
	lifecycle: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('lifecycle', form);
		if (!reason.ok) return reason.failure;
		const target = text(form, 'target_state') as AccountState;
		if (!STATES.includes(target))
			return invalid('lifecycle', { target_state: 'Reload the page and try again.' });
		if (target === 'retired' && text(form, 'confirm').trim() !== event.params.account_id)
			return invalid('lifecycle', { confirm: `Type ${event.params.account_id} to confirm.` });
		const res = await actionApi(event).call((c) =>
			c.POST('/accounts/{account_id}/lifecycle', {
				params: { path: { account_id: event.params.account_id } },
				body: { target_state: target, reason: reason.reason }
			})
		);
		return finish('lifecycle', res);
	}
} satisfies Actions;

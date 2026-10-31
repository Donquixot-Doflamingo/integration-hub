import { actionApi, finish, invalid, readReason, requireSession, text } from '$lib/server/actions';
import type { Actions } from './$types';
import { API_FIELDS, BEFORE_PREFIX, updateBody, valuesFromForm } from '../tenant-form';

// HUB-270: edit a tenant and disable / enable it (updateTenant, admin, merge-patch). Both send the
// ETag the page was loaded with as If-Match, so a concurrent change answers 412
// PRECONDITION_FAILED (the dialog offers Reload) instead of being overwritten.
export const actions = {
	update: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('update', form);
		if (!reason.ok) return reason.failure;
		const built = updateBody(
			valuesFromForm(form),
			valuesFromForm(form, BEFORE_PREFIX),
			reason.reason
		);
		if (!built.ok) return invalid('update', built.fields);
		const res = await actionApi(event).call((c) =>
			c.PATCH('/tenants/{tenant_id}', {
				params: { path: { tenant_id: event.params.tenant_id }, header: ifMatch(form) },
				body: built.body
			})
		);
		return finish('update', res, API_FIELDS);
	},

	setDisabled: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('setDisabled', form);
		if (!reason.ok) return reason.failure;
		const disabled = text(form, 'disabled');
		if (disabled !== 'true' && disabled !== 'false') {
			return invalid('setDisabled', { disabled: 'Reload the page and try again.' });
		}
		// Disabling makes every key of the tenant answer 401: the id must be typed (checked again here).
		if (disabled === 'true' && text(form, 'confirm').trim() !== event.params.tenant_id) {
			return invalid('setDisabled', { confirm: `Type ${event.params.tenant_id} to confirm.` });
		}
		const res = await actionApi(event).call((c) =>
			c.PATCH('/tenants/{tenant_id}', {
				params: { path: { tenant_id: event.params.tenant_id }, header: ifMatch(form) },
				body: { disabled: disabled === 'true', reason: reason.reason }
			})
		);
		return finish('setDisabled', res);
	}
} satisfies Actions;

/** `If-Match` from the hidden `if_match` field (the ETag of the loaded tenant), when there is one. */
function ifMatch(form: FormData): { 'If-Match'?: string } {
	const etag = text(form, 'if_match').trim();
	return etag ? { 'If-Match': etag } : {};
}

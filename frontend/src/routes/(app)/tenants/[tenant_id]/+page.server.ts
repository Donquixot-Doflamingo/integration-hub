import {
	actionApi,
	finish,
	invalid,
	readReason,
	requireEnhanced,
	requireSession,
	text
} from '$lib/server/actions';
import type { Actions } from './$types';
import { API_FIELDS, BEFORE_PREFIX, updateBody, valuesFromForm } from '../tenant-form';
import { createKeyBody, KEY_ID, keyValuesFromForm, overlapFromForm } from './keys';
import {
	API_FIELDS as ENDPOINT_FIELDS,
	createEndpointBody,
	endpointValuesFromForm,
	updateEndpointBody,
	withoutSigningSecret
} from './endpoints';
import { SLUG } from '../tenant-form';

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
	},

	// HUB-271: API keys (admin). createKey and rotateKey answer with the new secret, shown once:
	// only enhanced (fetch) submissions, so it never lands in a full page the browser keeps in its
	// history. The secret passes through untouched and is not logged.
	createKey: async (event) => {
		requireSession(event);
		const notEnhanced = requireEnhanced('createKey', event);
		if (notEnhanced) return notEnhanced;
		const form = await event.request.formData();
		const reason = readReason('createKey', form);
		if (!reason.ok) return reason.failure;
		const built = createKeyBody(event.params.tenant_id, keyValuesFromForm(form), reason.reason);
		if (!built.ok) return invalid('createKey', built.fields);
		const res = await actionApi(event).call((c) => c.POST('/api-keys', { body: built.body }));
		return finish('createKey', res, ['reason', 'scopes', 'description', 'expires_at']);
	},

	rotateKey: async (event) => {
		requireSession(event);
		const notEnhanced = requireEnhanced('rotateKey', event);
		if (notEnhanced) return notEnhanced;
		const form = await event.request.formData();
		const reason = readReason('rotateKey', form);
		if (!reason.ok) return reason.failure;
		const keyId = keyIdFrom(form);
		if (!keyId) return invalid('rotateKey', { key_id: 'Reload the page and pick the key again.' });
		const overlap = overlapFromForm(form);
		if (overlap === null) return invalid('rotateKey', { overlap_ms: 'Pick an overlap.' });
		const res = await actionApi(event).call((c) =>
			c.POST('/api-keys/{key_id}/rotate', {
				params: { path: { key_id: keyId } },
				body: { overlap_ms: overlap, reason: reason.reason }
			})
		);
		return finish('rotateKey', res, ['reason', 'overlap_ms']);
	},

	revokeKey: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('revokeKey', form);
		if (!reason.ok) return reason.failure;
		const keyId = keyIdFrom(form);
		if (!keyId) return invalid('revokeKey', { key_id: 'Reload the page and pick the key again.' });
		// The prefix must be typed (checked again here): revoking is immediate and final.
		const prefix = text(form, 'prefix');
		if (!prefix || text(form, 'confirm').trim() !== prefix) {
			return invalid('revokeKey', { confirm: `Type ${prefix || 'the key prefix'} to confirm.` });
		}
		const res = await actionApi(event).call((c) =>
			c.POST('/api-keys/{key_id}/revoke', {
				params: { path: { key_id: keyId } },
				body: { reason: reason.reason }
			})
		);
		return finish('revokeKey', res);
	},

	// HUB-272: registrar (callback) endpoints (admin). Create and rotate answer with the new
	// signing secret; it is dropped here (`withoutSigningSecret`): the console never displays it.
	// The SSRF guard's 400 pointer `/url` lands on the URL field.
	createEndpoint: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('createEndpoint', form);
		if (!reason.ok) return reason.failure;
		const built = createEndpointBody(
			event.params.tenant_id,
			endpointValuesFromForm(form),
			reason.reason
		);
		if (!built.ok) return invalid('createEndpoint', built.fields);
		const res = await actionApi(event).call((c) =>
			c.POST('/registrar-endpoints', { body: built.body })
		);
		return finish('createEndpoint', withoutSigningSecret(res), ENDPOINT_FIELDS);
	},

	updateEndpoint: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('updateEndpoint', form);
		if (!reason.ok) return reason.failure;
		const endpointId = endpointIdFrom(form);
		if (!endpointId) return invalid('updateEndpoint', { url: 'Reload the page and try again.' });
		const built = updateEndpointBody(
			endpointValuesFromForm(form),
			endpointValuesFromForm(form, BEFORE_PREFIX),
			reason.reason
		);
		if (!built.ok) return invalid('updateEndpoint', built.fields);
		const res = await actionApi(event).call((c) =>
			c.PATCH('/registrar-endpoints/{endpoint_id}', {
				params: { path: { endpoint_id: endpointId } },
				body: built.body
			})
		);
		return finish('updateEndpoint', res, ENDPOINT_FIELDS);
	},

	setEndpointDisabled: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('setEndpointDisabled', form);
		if (!reason.ok) return reason.failure;
		const endpointId = endpointIdFrom(form);
		const disabled = text(form, 'disabled');
		if (!endpointId || (disabled !== 'true' && disabled !== 'false')) {
			return invalid('setEndpointDisabled', { disabled: 'Reload the page and try again.' });
		}
		const res = await actionApi(event).call((c) =>
			c.PATCH('/registrar-endpoints/{endpoint_id}', {
				params: { path: { endpoint_id: endpointId } },
				body: { disabled: disabled === 'true', reason: reason.reason }
			})
		);
		return finish('setEndpointDisabled', res);
	},

	rotateSecret: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('rotateSecret', form);
		if (!reason.ok) return reason.failure;
		const endpointId = endpointIdFrom(form);
		if (!endpointId)
			return invalid('rotateSecret', { overlap_ms: 'Reload the page and try again.' });
		const overlap = overlapFromForm(form);
		if (overlap === null) return invalid('rotateSecret', { overlap_ms: 'Pick an overlap.' });
		const res = await actionApi(event).call((c) =>
			c.POST('/registrar-endpoints/{endpoint_id}/rotate-secret', {
				params: { path: { endpoint_id: endpointId } },
				body: { overlap_ms: overlap, reason: reason.reason }
			})
		);
		return finish('rotateSecret', withoutSigningSecret(res), ['reason', 'overlap_ms']);
	},

	deleteEndpoint: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('deleteEndpoint', form);
		if (!reason.ok) return reason.failure;
		const endpointId = endpointIdFrom(form);
		if (!endpointId)
			return invalid('deleteEndpoint', { confirm: 'Reload the page and try again.' });
		if (text(form, 'confirm').trim() !== endpointId) {
			return invalid('deleteEndpoint', { confirm: `Type ${endpointId} to confirm.` });
		}
		// DELETE carries the reason as a query parameter (contract ReasonQuery).
		const res = await actionApi(event).call((c) =>
			c.DELETE('/registrar-endpoints/{endpoint_id}', {
				params: { path: { endpoint_id: endpointId }, query: { reason: reason.reason } }
			})
		);
		return finish('deleteEndpoint', res);
	}
} satisfies Actions;

function endpointIdFrom(form: FormData): string | null {
	const id = text(form, 'endpoint_id');
	return SLUG.test(id) ? id : null;
}

function keyIdFrom(form: FormData): string | null {
	const id = text(form, 'key_id');
	return KEY_ID.test(id) ? id : null;
}

/** `If-Match` from the hidden `if_match` field (the ETag of the loaded tenant), when there is one. */
function ifMatch(form: FormData): { 'If-Match'?: string } {
	const etag = text(form, 'if_match').trim();
	return etag ? { 'If-Match': etag } : {};
}

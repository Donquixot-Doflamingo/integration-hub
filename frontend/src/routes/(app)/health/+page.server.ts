import {
	actionApi,
	finish,
	invalid,
	readReason,
	requireSession,
	text,
	type Form
} from '$lib/server/actions';
import type { RequestEvent } from '@sveltejs/kit';
import type { Actions } from './$types';
import { parseDuration } from './force';

// HUB-262: breaker force controls (contract `x-required-role: admin`), run on the server.
// The contract has no precondition for these (last write wins); clear-force answers 409 CONFLICT
// when the breaker is not forced.

function target(form: Form) {
	return {
		account_id: text(form, 'account_id').trim(),
		endpoint_ref: text(form, 'endpoint_ref').trim()
	};
}

async function force(event: RequestEvent, op: 'forceOpen' | 'forceClose') {
	requireSession(event);
	const form = await event.request.formData();
	const reason = readReason(op, form);
	if (!reason.ok) return reason.failure;
	const path = target(form);
	if (!path.account_id || !path.endpoint_ref) {
		return invalid(op, { confirm: 'Reload the page: the breaker is not known.' });
	}
	// Typed confirmation (F-19 rule 6): the account id, checked again on the server.
	if (text(form, 'confirm').trim() !== path.account_id) {
		return invalid(op, { confirm: `Type ${path.account_id} to confirm.` });
	}
	const duration = parseDuration(text(form, 'duration_ms'));
	if (!duration.ok) return invalid(op, { duration_ms: duration.message });
	const body = { duration_ms: duration.value, reason: reason.reason };
	const api = actionApi(event);
	const res =
		op === 'forceOpen'
			? await api.call((c) =>
					c.POST('/breakers/{account_id}/{endpoint_ref}/force-open', { params: { path }, body })
				)
			: await api.call((c) =>
					c.POST('/breakers/{account_id}/{endpoint_ref}/force-close', { params: { path }, body })
				);
	return finish(op, res, ['reason', 'duration_ms']);
}

export const actions = {
	forceOpen: (event) => force(event, 'forceOpen'),
	forceClose: (event) => force(event, 'forceClose'),
	clearForce: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('clearForce', form);
		if (!reason.ok) return reason.failure;
		const path = target(form);
		if (!path.account_id || !path.endpoint_ref) {
			return invalid('clearForce', { reason: 'Reload the page: the breaker is not known.' });
		}
		const res = await actionApi(event).call((c) =>
			c.POST('/breakers/{account_id}/{endpoint_ref}/clear-force', {
				params: { path },
				body: { reason: reason.reason }
			})
		);
		return finish('clearForce', res);
	}
} satisfies Actions;

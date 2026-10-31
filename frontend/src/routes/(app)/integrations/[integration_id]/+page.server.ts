import { actionApi, finish, int, invalid, readReason, requireSession } from '$lib/server/actions';
import type { RequestEvent } from '@sveltejs/kit';
import type { Actions } from './$types';
import { weightsFromForm } from './weights';

// Mutations on the integration detail page, run on the server (token stays server side).
// HUB-259: rollback-as-new-version. HUB-260: weights (new version), pause and resume.
// Concurrency: rollback and weights send `expected_active_version` (409 CONFLICT if another
// publish happened first). Pause and resume take no precondition in the contract (pausing twice
// is a logged no-op).
export const actions = {
	rollback: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('rollback', form);
		if (!reason.ok) return reason.failure;
		const toVersion = int(form, 'to_version');
		const expected = int(form, 'expected_active_version');
		if (!toVersion || toVersion < 1) {
			return invalid('rollback', { to_version: 'Pick the version to roll back to.' });
		}
		if (!expected || expected < 1) {
			return invalid('rollback', {
				expected_active_version: 'Reload the page: the active version is not known.'
			});
		}
		const res = await actionApi(event).call((c) =>
			c.POST('/integrations/{integration_id}/rollback', {
				params: { path: { integration_id: event.params.integration_id } },
				body: { to_version: toVersion, expected_active_version: expected, reason: reason.reason }
			})
		);
		return finish('rollback', res);
	},

	weights: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('weights', form);
		if (!reason.ok) return reason.failure;
		const expected = int(form, 'expected_active_version');
		if (!expected || expected < 1) {
			return invalid('weights', {
				weights: 'Reload the page: there is no active version to copy.'
			});
		}
		const parsed = weightsFromForm(form);
		if (!parsed.ok) return invalid('weights', { weights: parsed.message });
		const res = await actionApi(event).call((c) =>
			c.POST('/integrations/{integration_id}/weights', {
				params: { path: { integration_id: event.params.integration_id } },
				body: {
					expected_active_version: expected,
					weights: parsed.weights,
					publish: form.get('publish') === 'on',
					reason: reason.reason
				}
			})
		);
		return finish('weights', res, ['reason', 'weights']);
	},

	pause: (event) => switchPause(event, 'pause'),
	resume: (event) => switchPause(event, 'resume')
} satisfies Actions;

async function switchPause(
	event: RequestEvent<{ integration_id: string }>,
	op: 'pause' | 'resume'
) {
	requireSession(event);
	const form = await event.request.formData();
	const reason = readReason(op, form);
	if (!reason.ok) return reason.failure;
	const path = { integration_id: event.params.integration_id };
	const body = { reason: reason.reason };
	const api = actionApi(event);
	const res =
		op === 'pause'
			? await api.call((c) =>
					c.POST('/integrations/{integration_id}/pause', { params: { path }, body })
				)
			: await api.call((c) =>
					c.POST('/integrations/{integration_id}/resume', { params: { path }, body })
				);
	return finish(op, res);
}

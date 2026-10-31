import { actionApi, finish, int, invalid, readReason, requireSession } from '$lib/server/actions';
import type { Actions } from './$types';

// Mutations on the integration detail page, run on the server (token stays server side).
// HUB-259: rollback-as-new-version. Concurrency: `expected_active_version` (409 CONFLICT if
// another publish happened first).
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
	}
} satisfies Actions;

import { fail } from '@sveltejs/kit';
import { toPlainError, type ActionSuccessData } from '$lib/api/actions';
import type { IntegrationSpec, ValidationReport } from '$lib/api/types';
import {
	actionApi,
	finish,
	int,
	invalid,
	readReason,
	requireSession,
	text
} from '$lib/server/actions';
import { parseSpecYaml } from '$lib/utils/spec-yaml';
import type { Actions } from './$types';
import type { SavedDraft } from './draft';

// HUB-259 publish flow, server side (the admin token never reaches the browser):
// saveDraft (create or replace the draft spec, then run the checks), validate, publish.
export const actions = {
	saveDraft: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const parsed = parseSpecYaml(text(form, 'spec'));
		if (!parsed.ok) {
			const at = parsed.line ? `Line ${parsed.line}: ` : '';
			return invalid('saveDraft', { spec: `${at}${parsed.message}` });
		}
		const reason = readReason('saveDraft', form);
		if (!reason.ok) return reason.failure;
		const spec = parsed.value as unknown as IntegrationSpec;
		const integration_id = event.params.integration_id;
		const api = actionApi(event);
		const version = int(form, 'version');
		const res = version
			? await api.call((c) =>
					c.PUT('/integrations/{integration_id}/versions/{version}', {
						params: { path: { integration_id, version } },
						body: { spec, reason: reason.reason }
					})
				)
			: await api.call((c) =>
					c.POST('/integrations/{integration_id}/versions', {
						params: { path: { integration_id } },
						body: { based_on: int(form, 'based_on'), spec, reason: reason.reason }
					})
				);
		if (!res.ok) return finish('saveDraft', res, ['reason', 'spec']);
		const saved = res.data.integration_version.version;
		const check = await api.call((c) =>
			c.POST('/integrations/{integration_id}/versions/{version}/validate', {
				params: { path: { integration_id, version: saved } },
				body: {}
			})
		);
		return {
			op: 'saveDraft',
			changeId: res.data.change_id,
			requestId: res.requestId,
			result: { version: saved, report: check.ok ? check.data : null }
		} satisfies ActionSuccessData<SavedDraft>;
	},

	validate: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const version = int(form, 'version');
		if (!version) return invalid('validate', { version: 'Save the draft first.' });
		const res = await actionApi(event).call((c) =>
			c.POST('/integrations/{integration_id}/versions/{version}/validate', {
				params: { path: { integration_id: event.params.integration_id, version } },
				body: {}
			})
		);
		if (!res.ok) return fail(res.status || 502, { op: 'validate', error: toPlainError(res.error) });
		return {
			op: 'validate',
			changeId: null,
			requestId: res.requestId,
			result: res.data
		} satisfies ActionSuccessData<ValidationReport>;
	},

	publish: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('publish', form);
		if (!reason.ok) return reason.failure;
		const version = int(form, 'version');
		if (!version) return invalid('publish', { version: 'There is no draft to publish.' });
		// Empty = no active version yet (first publish): the contract wants null, not a number.
		const expectedRaw = text(form, 'expected_active_version').trim();
		const expected = expectedRaw === '' ? null : int(form, 'expected_active_version');
		if (expectedRaw !== '' && expected === null) {
			return invalid('publish', { expected_active_version: 'Reload the page and try again.' });
		}
		const res = await actionApi(event).call((c) =>
			c.POST('/integrations/{integration_id}/versions/{version}/publish', {
				params: { path: { integration_id: event.params.integration_id, version } },
				body: { expected_active_version: expected, reason: reason.reason }
			})
		);
		return finish('publish', res);
	}
} satisfies Actions;

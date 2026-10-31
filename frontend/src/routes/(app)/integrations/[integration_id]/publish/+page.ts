import type { ApiResult } from '$lib/api/client';
import { consoleApi } from '$lib/api/console';
import type { Integration, IntegrationVersion, VersionDiff } from '$lib/api/types';
import type { PageLoad } from './$types';

export interface PublishBase {
	integration: Integration;
	/** The open draft (`has_draft`: it is `latest_version`), or null when there is none. */
	draft: IntegrationVersion | null;
}

// HUB-259 publish flow: the integration, its open draft (full spec) and the diff of the draft
// against the active version (shown before the confirm dialog, admin-console-ux §2).
export const load: PageLoad = (event) => {
	const api = consoleApi(event);
	const integration_id = event.params.integration_id;
	const integration = api.call((c) =>
		c.GET('/integrations/{integration_id}', { params: { path: { integration_id } } })
	);

	const base: Promise<ApiResult<PublishBase>> = integration.then(async (r) => {
		if (!r.ok) return r;
		const i = r.data;
		if (!i.has_draft || i.latest_version == null)
			return { ...r, data: { integration: i, draft: null } };
		const d = await api.call((c) =>
			c.GET('/integrations/{integration_id}/versions/{version}', {
				params: { path: { integration_id, version: i.latest_version! } }
			})
		);
		if (!d.ok) return d;
		return { ...r, data: { integration: i, draft: d.data.state === 'draft' ? d.data : null } };
	});

	const diff: Promise<ApiResult<VersionDiff> | null> = integration.then((r) => {
		if (
			!r.ok ||
			!r.data.has_draft ||
			r.data.active_version == null ||
			r.data.latest_version == null
		)
			return null;
		const { active_version: from, latest_version: to } = r.data;
		if (from === to) return null;
		return api.call((c) =>
			c.GET('/integrations/{integration_id}/diff', {
				params: { path: { integration_id }, query: { from, to } }
			})
		);
	});

	return { integrationId: integration_id, base, diff };
};

import { error } from '@sveltejs/kit';
import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';

// getIntegrationVersion: the full spec of any version, draft or published (HUB-257).
export const load: PageLoad = (event) => {
	const version = Number(event.params.version);
	if (!Number.isInteger(version) || version < 1) {
		error(404, {
			message: `"${event.params.version}" is not a version number.`,
			code: 'NOT_FOUND'
		});
	}
	const path = { integration_id: event.params.integration_id, version };
	return {
		integrationId: event.params.integration_id,
		versionNumber: version,
		version: consoleApi(event).call((c) =>
			c.GET('/integrations/{integration_id}/versions/{version}', { params: { path } })
		)
	};
};

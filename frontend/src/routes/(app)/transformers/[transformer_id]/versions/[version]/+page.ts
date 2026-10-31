import { error } from '@sveltejs/kit';
import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';

// One immutable transformer version (HUB-268): getTransformerVersion (YAML + fixtures).
export const load: PageLoad = (event) => {
	const { transformer_id } = event.params;
	const raw = event.params.version;
	const version = Number(raw);
	if (!/^[1-9][0-9]*$/.test(raw) || !Number.isSafeInteger(version)) {
		error(404, { message: `"${raw}" is not a version number.`, code: 'NOT_FOUND' });
	}
	return {
		transformerId: transformer_id,
		version,
		detail: consoleApi(event).call((c) =>
			c.GET('/transformers/{transformer_id}/versions/{version}', {
				params: { path: { transformer_id, version } }
			})
		)
	};
};

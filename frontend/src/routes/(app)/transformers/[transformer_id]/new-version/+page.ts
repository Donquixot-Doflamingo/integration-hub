import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';
import { authoringLookups } from '../../lookups';

// Next version of a transformer (HUB-293): the latest version in full (the form starts from it)
// and the form's suggestions, as one result.
export const load: PageLoad = (event) => {
	const api = consoleApi(event);
	const transformer_id = event.params.transformer_id;
	const latest = api.call((c) =>
		c.GET('/transformers/{transformer_id}', { params: { path: { transformer_id } } })
	);
	return {
		transformerId: transformer_id,
		start: Promise.all([latest, authoringLookups(api)]).then(([t, l]) => {
			if (!t.ok) return t;
			if (!l.ok) return l;
			return { ...t, data: { transformer: t.data, ...l.data } };
		})
	};
};

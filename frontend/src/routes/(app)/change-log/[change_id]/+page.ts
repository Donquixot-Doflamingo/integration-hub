import { error } from '@sveltejs/kit';
import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';

/** Contract `ChangeId`: `chg_` + ULID. */
const CHANGE_ID = /^chg_[0-9A-HJKMNP-TV-Z]{26}$/;

// One change log entry with before / after (HUB-274, getChangeLogEntry).
export const load: PageLoad = (event) => {
	const changeId = event.params.change_id;
	if (!CHANGE_ID.test(changeId)) {
		error(404, { message: `"${changeId}" is not a change log id.`, code: 'NOT_FOUND' });
	}
	return {
		changeId,
		entry: consoleApi(event).call((c) =>
			c.GET('/change-log/{change_id}', { params: { path: { change_id: changeId } } })
		)
	};
};

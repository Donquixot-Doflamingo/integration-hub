import { error } from '@sveltejs/kit';
import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';

const REQUEST_ID = /^req_[0-9A-HJKMNP-TV-Z]{26}$/;

// Request timeline (HUB-264): the request record, its audit events (DESIGN §13.1, `seq` order) and
// its upstream attempts, loaded in parallel. Payloads come back already shaped by the audit policy.
export const load: PageLoad = (event) => {
	const request_id = event.params.request_id;
	if (!REQUEST_ID.test(request_id)) {
		error(404, {
			message: `"${request_id}" is not a request id (req_ + 26 characters).`,
			code: 'NOT_FOUND'
		});
	}
	const api = consoleApi(event);
	const path = { request_id };
	return {
		requestId: request_id,
		request: api.call((c) => c.GET('/requests/{request_id}', { params: { path } })),
		timeline: api.call((c) => c.GET('/requests/{request_id}/timeline', { params: { path } })),
		attempts: api.call((c) => c.GET('/requests/{request_id}/attempts', { params: { path } }))
	};
};

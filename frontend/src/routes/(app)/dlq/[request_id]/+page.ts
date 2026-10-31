import { error } from '@sveltejs/kit';
import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';

const REQUEST_ID = /^req_[0-9A-HJKMNP-TV-Z]{26}$/;

// DLQ entry (HUB-266): getDeadLetter gives the reason, the canonical payload and the last vendor
// request and response (both shaped by the payload policy), and the attempt history.
export const load: PageLoad = (event) => {
	const request_id = event.params.request_id;
	if (!REQUEST_ID.test(request_id)) {
		error(404, {
			message: `"${request_id}" is not a request id (req_ + 26 characters).`,
			code: 'NOT_FOUND'
		});
	}
	return {
		requestId: request_id,
		entry: consoleApi(event).call((c) =>
			c.GET('/dlq/{request_id}', { params: { path: { request_id } } })
		)
	};
};

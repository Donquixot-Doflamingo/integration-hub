import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';

// listDeadLetters without filters (filters and replay arrive with the DLQ console ticket).
export const load: PageLoad = (event) => ({
	list: consoleApi(event).call((c) => c.GET('/dlq'))
});

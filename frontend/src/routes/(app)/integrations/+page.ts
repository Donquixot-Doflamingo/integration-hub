import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';

// listIntegrations. Not awaited, so the page shows its loading state while the call runs.
export const load: PageLoad = (event) => ({
	list: consoleApi(event).call((c) => c.GET('/integrations'))
});

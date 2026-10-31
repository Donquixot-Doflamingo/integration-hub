import { consoleApi } from '$lib/api/console';
import type { LayoutLoad } from './$types';

// The signed-in user for the user menu. Not awaited: the shell renders at once and the menu
// fills in when /me answers. (HUB-253 moves this server-side with the OIDC session.)
export const load: LayoutLoad = (event) => ({
	me: consoleApi(event).call((c) => c.GET('/me'))
});

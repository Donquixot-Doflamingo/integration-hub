import { redirect } from '@sveltejs/kit';
import { loginUrl } from '$lib/auth/redirect';
import { toPublicSession } from '$lib/server/auth/session';
import type { LayoutServerLoad } from './$types';

// Every console page requires a session (HUB-253). Unauthenticated users go to /login and come
// back to the page they asked for. Only the token-free view of the session reaches the browser.
export const load: LayoutServerLoad = ({ locals, url }) => {
	if (!locals.session) redirect(303, loginUrl(url));
	return { session: toPublicSession(locals.session) };
};

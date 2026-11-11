import { redirect } from '@sveltejs/kit';
import { env as publicEnv } from '$env/dynamic/public';
import { loginUrl } from '$lib/auth/redirect';
import { toPublicSession } from '$lib/server/auth/session';
import type { LayoutServerLoad } from './$types';

/** `PUBLIC_ISFC_ADMIN_URL` when it is an http(s) URL: where operators change ISFC-owned data. */
function isfcAdminUrl(): string | null {
	const raw = publicEnv.PUBLIC_ISFC_ADMIN_URL?.trim();
	if (!raw) return null;
	try {
		const u = new URL(raw);
		return u.protocol === 'https:' || u.protocol === 'http:' ? u.toString() : null;
	} catch {
		return null;
	}
}

// Every console page requires a session (HUB-253). Unauthenticated users go to /login and come
// back to the page they asked for. Only the token-free view of the session reaches the browser.
// `isfcMode` tells pages that tenants come from ISFC (HUB-485): they hide "Create tenant".
export const load: LayoutServerLoad = ({ locals, url }) => {
	if (!locals.session) redirect(303, loginUrl(url));
	return {
		session: toPublicSession(locals.session),
		isfcMode: publicEnv.PUBLIC_AUTH_MODE === 'isfc',
		isfcAdminUrl: isfcAdminUrl()
	};
};

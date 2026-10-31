import { redirect } from '@sveltejs/kit';
import { consoleApi } from '$lib/api/console';
import { accessFromMe } from '$lib/auth/roles';
import { loginUrl } from '$lib/auth/redirect';
import type { LayoutLoad } from './$types';

// `/me` decides what the UI shows (contract: "The console uses it to show or hide actions").
// Awaited, so no mutation control flashes before the role is known. If it fails, access is
// "unknown" and every gated control stays hidden (fail closed); 401 means the API no longer
// accepts the session, so sign in again.
export const load: LayoutLoad = async (event) => {
	const { session } = event.data;
	if (__API_MOCKING__) {
		// Mock mode: make MSW's /me answer as the identity chosen on the dev login page.
		const { useMockIdentity } = await import('$lib/api/mocks/browser');
		useMockIdentity(session.user);
	}
	const me = await consoleApi(event).call((c) => c.GET('/me'));
	if (!me.ok && me.error.is('UNAUTHORIZED')) redirect(303, loginUrl(event.url, { expired: true }));
	return { session, me, access: accessFromMe(me) };
};

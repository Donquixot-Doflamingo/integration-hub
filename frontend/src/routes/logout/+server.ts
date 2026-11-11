import { redirect } from '@sveltejs/kit';
import { readIsfcConfig } from '$lib/server/auth/config';
import { clearSessionCookie } from '$lib/server/auth/cookie';
import { logoutSession } from '$lib/server/auth/isfc';
import { refreshForEvent } from '$lib/server/auth/refresh';
import type { RequestHandler } from './$types';

// POST only (a sign-out link must not be triggerable by a GET or a prefetch). SvelteKit's CSRF
// origin check applies to this form post.
//
// With an ISFC session (HUB-492) the session is ended at ISFC first (`POST {auth}/api/v1/logout`
// with the access token), so the refresh token cannot be used again. Whatever ISFC answers, the
// console's own cookie is always cleared: a user can never be stuck signed in.
export const POST: RequestHandler = async (event) => {
	const { cookies, locals, request, fetch } = event;
	const session = locals.session;
	const config = readIsfcConfig();
	if (session?.provider === 'isfc' && session.isfcSessionId && config.ok) {
		try {
			// An expired access token is refreshed first, like the gateway's token handler does.
			await refreshForEvent(event, { fetch });
			const current = locals.session;
			if (current?.accessToken && current.isfcSessionId) {
				await logoutSession(config.config, fetch, {
					accessToken: current.accessToken,
					sessionId: current.isfcSessionId,
					deviceId: current.deviceId ?? '',
					userAgent: request.headers.get('user-agent')
				});
			}
		} catch {
			/* ISFC did not answer or refused: the local session still ends below. */
		}
	}
	clearSessionCookie(cookies);
	redirect(303, '/login');
};

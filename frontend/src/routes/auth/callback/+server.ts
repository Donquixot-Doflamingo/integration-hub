import { error, redirect } from '@sveltejs/kit';
import { authMode, sessionSecret } from '$lib/server/auth';
import { readIsfcConfig } from '$lib/server/auth/config';
import { writeSessionCookie } from '$lib/server/auth/cookie';
import { deviceIdOf } from '$lib/server/auth/device';
import { LOGIN_COOKIE, LOGIN_COOKIE_PATH, completeLogin } from '$lib/server/auth/login-flow';
import type { RequestHandler } from './$types';

/**
 * Where Hydra returns after Email OTP or Microsoft (`?code=&state=`; HUB-491). The server checks
 * `state` against the encrypted `hub_login` cookie, exchanges the code at ISFC (with the browser's
 * `x-device-id` and User-Agent) and sets the session cookie. It always answers with a redirect, so
 * `code` and `state` never stay in the address bar; a failure goes to `/login?error=<code>`.
 */
export const GET: RequestHandler = async ({ url, cookies, request, fetch }) => {
	if (authMode() !== 'isfc') error(404, { message: 'Not found' });
	const secret = sessionSecret();
	const config = readIsfcConfig();
	const loginCookie = cookies.get(LOGIN_COOKIE);
	// The login state is single use, whatever happens next.
	cookies.delete(LOGIN_COOKIE, { path: LOGIN_COOKIE_PATH, secure: url.protocol === 'https:' });
	if (!secret || !config.ok) redirect(303, '/login?error=sign_in_failed');

	const secure = url.protocol === 'https:';
	const result = await completeLogin(config.config, fetch, {
		code: url.searchParams.get('code'),
		state: url.searchParams.get('state'),
		error: url.searchParams.get('error'),
		errorCode: url.searchParams.get('error_code'),
		loginCookie,
		origin: url.origin,
		deviceId: deviceIdOf(cookies, secure),
		userAgent: request.headers.get('user-agent'),
		secret
	});
	if (!result.ok) redirect(303, `/login?error=${encodeURIComponent(result.code)}`);
	writeSessionCookie(cookies, result.session, secret, secure);
	redirect(303, result.next);
};

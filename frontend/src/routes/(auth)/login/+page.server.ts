import { fail, redirect } from '@sveltejs/kit';
import { isfcErrorMessage } from '$lib/auth/isfc-errors';
import { safeNext } from '$lib/auth/redirect';
import { authMode, authProvider, sessionSecret } from '$lib/server/auth';
import { readIsfcConfig } from '$lib/server/auth/config';
import { writeSessionCookie } from '$lib/server/auth/cookie';
import { deviceIdOf } from '$lib/server/auth/device';
import {
	LOGIN_COOKIE,
	LOGIN_COOKIE_PATH,
	LOGIN_TTL_SECONDS,
	sealLoginState,
	startLogin
} from '$lib/server/auth/login-flow';
import { newSession } from '$lib/server/auth/session';
import { ENHANCED_HEADER } from '$lib/server/actions';
import type { Actions, PageServerLoad } from './$types';

export const load: PageServerLoad = async ({ url, locals }) => {
	const view = await authProvider().describe();
	const secretMissing = sessionSecret() === null;
	const code = url.searchParams.get('error');
	return {
		login: {
			...view,
			ready: view.ready && !secretMissing,
			missing: secretMissing ? [...view.missing, 'HUB_SESSION_SECRET'] : view.missing
		},
		next: safeNext(url.searchParams.get('next')),
		// `?expired=1` (the API rejected the session), or a session cookie that no longer verified.
		expired: url.searchParams.get('expired') === '1' || locals.sessionEnded,
		// Only messages the console knows: nothing from the query string is echoed.
		error: isfcErrorMessage(code) ? { code: code!, message: isfcErrorMessage(code)! } : null,
		signedInAs: locals.session ? (locals.session.user.name ?? locals.session.user.subject) : null
	};
};

// Plain POST form actions, deliberately without `use:enhance` where a redirect must work without
// JavaScript (see ADR-0009). SvelteKit does not allow a default action next to named ones, so both
// are named: `dev` is the role picker (mock builds and tests only); `start` begins an ISFC
// sign-in (HUB-491, HUB-493).
export const actions: Actions = {
	dev: async ({ request, cookies, url }) => {
		const form = await request.formData();
		const values = {
			role: String(form.get('role') ?? ''),
			name: String(form.get('name') ?? ''),
			email: String(form.get('email') ?? ''),
			tenants: String(form.get('tenants') ?? '')
		};
		const secret = sessionSecret();
		if (!secret) {
			return fail(503, {
				values,
				field: null,
				message: 'Sign-in is not configured on this server.'
			});
		}
		const result = await authProvider().signIn(form);
		if (!result.ok) {
			return fail(result.status, { values, field: result.field ?? null, message: result.message });
		}
		writeSessionCookie(cookies, newSession(result.session), secret, url.protocol === 'https:');
		redirect(303, safeNext(String(form.get('next') ?? '')));
	},

	/**
	 * Starts an ISFC sign-in: PKCE (S256), `state` and `nonce` go into the short-lived encrypted
	 * `hub_login` cookie. Email OTP answers 303 to Hydra; Microsoft answers the plan the browser
	 * needs (it must fetch Hydra's flow token itself, see `$lib/auth/microsoft`).
	 */
	start: async ({ request, cookies, url, fetch }) => {
		const form = await request.formData();
		const method = String(form.get('method') ?? '');
		const fail503 = (message: string) => fail(503, { values: null, field: null, message });
		const secret = sessionSecret();
		const config = readIsfcConfig();
		if (authMode() !== 'isfc' || !secret || !config.ok) {
			return fail503('Sign-in with ISFC is not configured on this server.');
		}
		const secure = url.protocol === 'https:';
		const result = await startLogin(config.config, fetch, {
			method,
			next: String(form.get('next') ?? ''),
			origin: url.origin,
			deviceId: deviceIdOf(cookies, secure)
		});
		if (!result.ok) {
			return fail(result.status, { values: null, field: null, message: result.message });
		}
		if (result.kind === 'microsoft' && request.headers.get(ENHANCED_HEADER) !== 'true') {
			return fail(400, {
				values: null,
				field: null,
				message: 'Microsoft sign-in needs JavaScript in your browser. Use Email OTP instead.'
			});
		}
		cookies.set(LOGIN_COOKIE, sealLoginState(result.loginState, secret), {
			path: LOGIN_COOKIE_PATH,
			httpOnly: true,
			sameSite: 'lax',
			secure,
			maxAge: LOGIN_TTL_SECONDS
		});
		if (result.kind === 'redirect') redirect(303, result.url);
		return { plan: result.plan };
	}
};

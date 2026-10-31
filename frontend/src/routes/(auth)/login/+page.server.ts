import { fail, redirect } from '@sveltejs/kit';
import { safeNext } from '$lib/auth/redirect';
import {
	SESSION_COOKIE,
	SESSION_TTL_SECONDS,
	authProvider,
	encodeSession,
	newSession,
	sessionSecret
} from '$lib/server/auth';
import type { Actions, PageServerLoad } from './$types';

export const load: PageServerLoad = ({ url, locals }) => {
	const view = authProvider().describe();
	const secretMissing = sessionSecret() === null;
	return {
		login: {
			...view,
			ready: view.ready && !secretMissing,
			missing: secretMissing ? [...view.missing, 'HUB_SESSION_SECRET'] : view.missing
		},
		next: safeNext(url.searchParams.get('next')),
		expired: url.searchParams.get('expired') === '1',
		signedInAs: locals.session ? (locals.session.user.name ?? locals.session.user.subject) : null
	};
};

// A plain POST form action, deliberately without `use:enhance` (see ADR-0009).
export const actions: Actions = {
	default: async ({ request, cookies, url }) => {
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
		cookies.set(SESSION_COOKIE, encodeSession(newSession(result.session), secret), {
			path: '/',
			httpOnly: true,
			sameSite: 'lax',
			secure: url.protocol === 'https:',
			maxAge: SESSION_TTL_SECONDS
		});
		redirect(303, safeNext(String(form.get('next') ?? '')));
	}
};

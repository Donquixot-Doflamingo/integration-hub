import { redirect } from '@sveltejs/kit';
import { SESSION_COOKIE } from '$lib/server/auth/session';
import type { RequestHandler } from './$types';

// POST only (a sign-out link must not be triggerable by a GET or a prefetch). SvelteKit's CSRF
// origin check applies to this form post.
export const POST: RequestHandler = ({ cookies }) => {
	cookies.delete(SESSION_COOKIE, { path: '/' });
	redirect(303, '/login');
};

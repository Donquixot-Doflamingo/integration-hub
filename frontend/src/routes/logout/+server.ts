import { redirect } from '@sveltejs/kit';
import { clearSessionCookie } from '$lib/server/auth/cookie';
import type { RequestHandler } from './$types';

// POST only (a sign-out link must not be triggerable by a GET or a prefetch). SvelteKit's CSRF
// origin check applies to this form post.
export const POST: RequestHandler = ({ cookies }) => {
	clearSessionCookie(cookies);
	redirect(303, '/login');
};

import type { Handle } from '@sveltejs/kit';

/**
 * True for request paths that start with two slashes (`//evil.example/x`).
 *
 * SvelteKit 2.x advisory GHSA-pfhg-q3qv-9q6r: an enhanced form submission (`use:enhance`) on a
 * route that accepts paths beginning with `//` can navigate to another origin. The fix only ships
 * in SvelteKit 3; the advisory's documented workaround is to reject such paths before routing.
 * See ADR-0009.
 */
export function isDoubleSlashPath(pathname: string): boolean {
	return pathname.startsWith('//');
}

/** `handle` step: answers 404 for `//`-prefixed paths before SvelteKit resolves any route. */
export const rejectDoubleSlashPaths: Handle = async ({ event, resolve }) => {
	if (isDoubleSlashPath(event.url.pathname)) {
		return new Response('Not found', {
			status: 404,
			headers: { 'content-type': 'text/plain; charset=utf-8' }
		});
	}
	return resolve(event);
};

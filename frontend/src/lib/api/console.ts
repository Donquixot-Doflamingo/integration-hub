import { env } from '$env/dynamic/public';
import { adminClient, DEFAULT_ADMIN_API_BASE, type AdminClient } from './client';

/** Admin API base for this deployment (`PUBLIC_ADMIN_API_BASE`, F-19 config), default `/admin/v1`. */
export function adminApiBase(): string {
	return env.PUBLIC_ADMIN_API_BASE || DEFAULT_ADMIN_API_BASE;
}

/**
 * The client for a `load` function: uses SvelteKit's `fetch` and resolves a relative base against
 * the page origin. Auth (OIDC token from the session) arrives with HUB-253.
 */
export function consoleApi(event: { fetch: typeof fetch; url: URL }): AdminClient {
	return adminClient({ fetch: event.fetch, origin: event.url.origin, baseUrl: adminApiBase() });
}

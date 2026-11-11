import type { Handle } from '@sveltejs/kit';
import { authMode } from '$lib/server/auth';
import { readIsfcConfig } from '$lib/server/auth/config';

/**
 * Content-Security-Policy, runtime half (HUB-499, gap G12). The static policy is `kit.csp` in
 * svelte.config.js (SvelteKit adds the script nonces). What the ISFC login needs from the browser
 * is runtime config, so this step adds it to the header SvelteKit built:
 *
 *   - `connect-src`: the Microsoft step fetches Hydra's authorization URL from the browser to get
 *     the flow token (src/lib/auth/microsoft.ts), so the ISFC auth origin;
 *   - `form-action`: the login form posts to the console, which answers 303 to Hydra, and browsers
 *     check the redirect target against `form-action` too, so the same origin.
 *
 * Entra needs nothing: the browser only navigates there (`location.assign`), which no directive
 * restricts. Outside ISFC mode (dev picker, mock builds) the policy stays self-only.
 */
const WIDENED = ['connect-src', 'form-action'] as const;

/** Adds `origins` to `connect-src` and `form-action` of a CSP header value (one entry per origin). */
export function withExtraOrigins(csp: string, origins: string[]): string {
	if (origins.length === 0) return csp;
	return csp
		.split(';')
		.map((part) => {
			const d = part.trim();
			const name = d.split(/\s+/)[0] ?? '';
			if (!(WIDENED as readonly string[]).includes(name)) return d;
			const have = new Set(d.split(/\s+/).slice(1));
			return [d, ...origins.filter((o) => !have.has(o))].join(' ');
		})
		.filter(Boolean)
		.join('; ');
}

/** The origin (no path) of the ISFC auth host, or none when the console does not use ISFC. */
export function isfcOrigins(): string[] {
	if (authMode() !== 'isfc') return [];
	const cfg = readIsfcConfig();
	return cfg.ok ? [new URL(cfg.config.authUrl).origin] : [];
}

/** `handle` step: widens the CSP SvelteKit set on page responses with the ISFC auth origin. */
export const widenCsp: Handle = async ({ event, resolve }) => {
	const response = await resolve(event);
	const csp = response.headers.get('content-security-policy');
	if (csp) response.headers.set('content-security-policy', withExtraOrigins(csp, isfcOrigins()));
	return response;
};

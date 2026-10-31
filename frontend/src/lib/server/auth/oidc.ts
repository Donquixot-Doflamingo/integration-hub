import type { AuthProvider } from './provider';

/** OIDC settings (private env, server only). Values are never sent to the browser or logged. */
export interface OidcConfig {
	issuer?: string;
	clientId?: string;
	redirectUri?: string;
}

/** Names of the env keys the OIDC provider needs (shown on the login page when missing). */
export const OIDC_ENV = {
	issuer: 'HUB_OIDC_ISSUER',
	clientId: 'HUB_OIDC_CLIENT_ID',
	redirectUri: 'HUB_OIDC_REDIRECT_URI'
} as const;

/**
 * Placeholder OIDC provider (Authorization Code + PKCE, ADR-0005). It is selected and configured by
 * env, reports missing settings, and refuses sign-in: the Hub's issuer (`hub admin` OIDC stub,
 * DESIGN §19 M7) does not exist yet. Real wiring: discovery, PKCE, callback, token in the session
 * cookie, `/me` after login, refresh — HUB-282.
 */
export function oidcProvider(config: OidcConfig): AuthProvider {
	const missing = (Object.keys(OIDC_ENV) as (keyof OidcConfig)[])
		.filter((k) => !config[k])
		.map((k) => OIDC_ENV[k]);
	const notice =
		'Single sign-on is not connected yet: the console waits for the Hub OIDC issuer (HUB-282).' +
		(missing.length ? ' Missing settings are listed below.' : '');
	return {
		id: 'oidc',
		describe: () => ({ provider: 'oidc', ready: false, notice, missing }),
		async signIn() {
			return { ok: false, status: 503, message: notice };
		}
	};
}

import { randomBytes } from 'node:crypto';
import { env as privateEnv } from '$env/dynamic/private';
import { env as publicEnv } from '$env/dynamic/public';
import { devProvider } from './dev';
import { oidcProvider } from './oidc';
import type { AuthProvider } from './provider';
import type { AuthProviderId } from './session';

export * from './session';
export type { AuthProvider, LoginView, SignInResult } from './provider';

/**
 * Which provider this deployment uses: `dev` in mock builds (`HUB_API_MOCKING=true`) or when
 * `PUBLIC_AUTH_MODE=dev`; otherwise `oidc`.
 */
export function authMode(): AuthProviderId {
	if (__API_MOCKING__ || publicEnv.PUBLIC_AUTH_MODE === 'dev') return 'dev';
	return 'oidc';
}

export function authProvider(): AuthProvider {
	if (authMode() === 'dev') return devProvider;
	return oidcProvider({
		issuer: privateEnv.HUB_OIDC_ISSUER,
		clientId: privateEnv.HUB_OIDC_CLIENT_ID,
		redirectUri: privateEnv.HUB_OIDC_REDIRECT_URI
	});
}

let devSecret: string | undefined;

/**
 * HMAC key for the session cookie: `HUB_SESSION_SECRET` (at least 32 characters). Dev mode without
 * one uses a random per-process key (sessions end when the server restarts). OIDC mode without one
 * returns null, and nobody can sign in (fail closed).
 */
export function sessionSecret(): string | null {
	const configured = privateEnv.HUB_SESSION_SECRET;
	if (configured && configured.length >= 32) return configured;
	if (authMode() !== 'dev') return null;
	devSecret ??= randomBytes(32).toString('base64url');
	return devSecret;
}

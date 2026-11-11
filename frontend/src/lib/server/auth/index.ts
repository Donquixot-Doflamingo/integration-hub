import { randomBytes } from 'node:crypto';
import { env as privateEnv } from '$env/dynamic/private';
import { env as publicEnv } from '$env/dynamic/public';
import { createDevProvider } from './dev';
import { isfcProvider } from './isfc-provider';
import type { AuthProvider } from './provider';
import type { AuthProviderId } from './session';

export * from './session';
export type { AuthProvider, LoginMethodView, LoginView, SignInResult } from './provider';

/** Name of the setting that picks the provider (public: the page may show it, it holds no secret). */
export const AUTH_MODE_ENV = 'PUBLIC_AUTH_MODE';

export type ModeResolution =
	| { mode: 'dev'; reason?: undefined }
	| { mode: 'isfc'; reason?: undefined }
	| { mode: null; reason: string };

/**
 * `PUBLIC_AUTH_MODE=dev|isfc` (HUB-495, AUTH_PLAN §5):
 * - mock builds (`__API_MOCKING__`) always use `dev`: there is no identity provider and no backend;
 * - `isfc` is the real login;
 * - `dev` is refused unless `NODE_ENV=test`, so the role picker can never run in a real
 *   deployment (it would let anyone choose their own role);
 * - anything else, including unset, is refused: the setting must be chosen on purpose.
 */
export function resolveAuthMode(opts: {
	mocking: boolean;
	mode: string | undefined;
	nodeEnv: string | undefined;
}): ModeResolution {
	if (opts.mocking) return { mode: 'dev' };
	const mode = opts.mode?.trim();
	if (mode === 'isfc') return { mode: 'isfc' };
	if (mode === 'dev') {
		return opts.nodeEnv === 'test'
			? { mode: 'dev' }
			: {
					mode: null,
					reason:
						'PUBLIC_AUTH_MODE=dev is only allowed in mock builds and tests: the role picker would let anyone pick a role. Use PUBLIC_AUTH_MODE=isfc.'
				};
	}
	return {
		mode: null,
		reason: mode
			? `PUBLIC_AUTH_MODE=${mode} is not a mode. Use isfc.`
			: 'PUBLIC_AUTH_MODE is not set. Set it to isfc.'
	};
}

/** The mode of this deployment, or why none is usable (see `resolveAuthMode`). */
export function authModeResolution(): ModeResolution {
	return resolveAuthMode({
		mocking: __API_MOCKING__,
		mode: publicEnv.PUBLIC_AUTH_MODE,
		nodeEnv: process.env.NODE_ENV
	});
}

/** The provider this deployment uses, or null when the mode is refused or unset. */
export function authMode(): AuthProviderId | null {
	return authModeResolution().mode;
}

export function authProvider(): AuthProvider {
	const r = authModeResolution();
	if (r.mode === 'dev') {
		// Against a running Hub the admin's dev issuer mints the token (HUB-286); tests only.
		return createDevProvider({
			issuer: __API_MOCKING__ ? undefined : privateEnv.HUB_DEV_OIDC_ISSUER
		});
	}
	if (r.mode === 'isfc') return isfcProvider();
	const reason = r.reason;
	return {
		id: 'none',
		describe: () => ({
			provider: 'none',
			ready: false,
			notice: reason,
			missing: [AUTH_MODE_ENV],
			methods: []
		}),
		async signIn() {
			return { ok: false, status: 503, message: reason };
		}
	};
}

let devSecret: string | undefined;

/**
 * Key for the sealed cookies: `HUB_SESSION_SECRET` (at least 32 characters). The dev provider
 * (mock builds and tests only) without one uses a random per-process key (sessions end when the
 * server restarts). Any other mode without one returns null, and nobody can sign in (fail closed).
 */
export function sessionSecret(): string | null {
	const configured = privateEnv.HUB_SESSION_SECRET;
	if (configured && configured.length >= 32) return configured;
	if (authMode() !== 'dev') return null;
	devSecret ??= randomBytes(32).toString('base64url');
	return devSecret;
}

/**
 * Start-up check (SvelteKit `init`, HUB-495): a deployed console must not run with the dev role
 * picker, so an explicit `PUBLIC_AUTH_MODE=dev` outside mock builds and tests stops the server
 * from starting. An unset or unknown mode starts, says why in the log, and nobody can sign in.
 */
export function assertAuthModeAtStartup(log: (msg: string) => void = console.error): void {
	const r = authModeResolution();
	if (r.mode !== null) return;
	if (publicEnv.PUBLIC_AUTH_MODE?.trim() === 'dev') throw new Error(r.reason);
	log(`[console] ${r.reason} Nobody can sign in until it is fixed.`);
}

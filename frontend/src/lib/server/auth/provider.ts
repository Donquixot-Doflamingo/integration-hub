import type { Session, AuthProviderId } from './session';

/** What the login page shows for the active provider. */
export interface LoginView {
	provider: AuthProviderId;
	/** False when sign-in cannot work (missing config, or pending the real issuer). */
	ready: boolean;
	/** One sentence for the operator, e.g. why sign-in is unavailable. */
	notice: string | null;
	/** Config keys that are missing (names only, never values). */
	missing: string[];
}

export type SignInResult =
	| { ok: true; session: Pick<Session, 'provider' | 'user' | 'accessToken'> }
	| {
			ok: false;
			status: number;
			message: string;
			/** Form field the message belongs to. */
			field?: string;
	  };

/**
 * One way to authenticate operators. The console picks one per deployment (`authProvider()`).
 * - `dev`: role picker, for mock mode and local work (no identity provider involved).
 * - `oidc`: Authorization Code + PKCE against the Hub's issuer. Placeholder until HUB-282.
 */
export interface AuthProvider {
	readonly id: AuthProviderId;
	describe(): LoginView;
	/** Handles the login form post. */
	signIn(form: FormData): Promise<SignInResult>;
}

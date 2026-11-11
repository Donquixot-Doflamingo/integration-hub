import type { Session, AuthProviderId } from './session';

/** A way to sign in that the login page offers (ISFC: Email OTP, Microsoft). */
export interface LoginMethodView {
	id: 'EMAIL_OTP' | 'MICROSOFT_SSO';
	/** Button label. */
	label: string;
}

/** What the login page shows for the active provider. */
export interface LoginView {
	/** `none`: no provider is usable (mode unset, or `dev` refused outside mock builds and tests). */
	provider: AuthProviderId | 'none';
	/** False when sign-in cannot work (missing config, ISFC unreachable, mode refused). */
	ready: boolean;
	/** One sentence for the operator, e.g. why sign-in is unavailable. */
	notice: string | null;
	/** Config keys that are missing (names only, never values). */
	missing: string[];
	/** ISFC: the sign-in methods to offer. Empty for the dev provider. */
	methods: LoginMethodView[];
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
 * - `dev`: role picker, for mock builds and tests only (no identity provider involved).
 * - `isfc`: ISFC login (HUB-491): Email OTP and Microsoft through Hydra; the form posts start
 *   the flow and `/auth/callback` finishes it, so `signIn` is not used.
 */
export interface AuthProvider {
	readonly id: AuthProviderId | 'none';
	describe(): LoginView | Promise<LoginView>;
	/** Handles the dev login form post. */
	signIn(form: FormData): Promise<SignInResult>;
}

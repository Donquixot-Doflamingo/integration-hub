import { randomUUID, timingSafeEqual } from 'node:crypto';
import type { SessionUser } from '$lib/api/types';
import type { MicrosoftPlan } from '$lib/auth/microsoft';
import { safeNext } from '$lib/auth/redirect';
import { isKnownErrorCode, knownErrorCode } from '$lib/auth/isfc-errors';
import { redirectUriFor, type IsfcConfig } from './config';
import { open, seal } from './crypto';
import {
	METHOD_EMAIL_OTP,
	METHOD_MICROSOFT,
	authorizeUrl,
	exchangeCode,
	fetchLoginMethods,
	generatePkce,
	IsfcError,
	jwtClaims,
	randomToken,
	type ConsoleMethodId,
	type Fetch,
	type LoginMethod,
	type TokenSet
} from './isfc';
import { newSession, type Session } from './session';

/** Short-lived, encrypted, httpOnly cookie that carries PKCE, `state` and `nonce` across the redirect. */
export const LOGIN_COOKIE = 'hub_login';
/** Only the callback needs it. */
export const LOGIN_COOKIE_PATH = '/auth';
export const LOGIN_TTL_SECONDS = 10 * 60;
/** Stable per-browser id sent to ISFC as `x-device-id`; auth-service binds the session to it. */
export const DEVICE_COOKIE = 'hub_device';
export const DEVICE_TTL_SECONDS = 365 * 24 * 60 * 60;

export interface LoginState {
	v: 1;
	state: string;
	nonce: string;
	verifier: string;
	method: ConsoleMethodId;
	/** Where to go after sign-in (already passed through `safeNext`). */
	next: string;
	/** Epoch seconds. */
	iat: number;
}

export function sealLoginState(s: LoginState, secret: string): string {
	return seal(s, secret, 'login');
}

/** The login state, or null when missing, tampered, malformed or older than 10 minutes. */
export function openLoginState(
	value: string | undefined,
	secret: string,
	now = Date.now()
): LoginState | null {
	const v = open(value, secret, 'login') as Partial<LoginState> | null;
	if (
		!v ||
		v.v !== 1 ||
		typeof v.state !== 'string' ||
		typeof v.nonce !== 'string' ||
		typeof v.verifier !== 'string' ||
		(v.method !== METHOD_EMAIL_OTP && v.method !== METHOD_MICROSOFT) ||
		typeof v.next !== 'string' ||
		typeof v.iat !== 'number' ||
		now / 1000 - v.iat > LOGIN_TTL_SECONDS
	) {
		return null;
	}
	return v as LoginState;
}

/** A new device id (`device-<uuid>`, the shape ISFC's dummy panel uses). */
export function newDeviceId(): string {
	return `device-${randomUUID()}`;
}

const DEVICE_ID = /^device-[0-9a-f-]{36}$/;
export const isDeviceId = (v: unknown): v is string => typeof v === 'string' && DEVICE_ID.test(v);

// ------------------------------------------------------------------------------------ start

export type StartResult =
	| { ok: true; kind: 'redirect'; url: string; loginState: LoginState }
	| { ok: true; kind: 'microsoft'; plan: MicrosoftPlan; loginState: LoginState }
	| { ok: false; status: number; message: string };

export interface StartInput {
	method: string;
	next: string | null | undefined;
	origin: string;
	deviceId: string;
}

const TENANT_ID = /^[0-9A-Za-z][0-9A-Za-z.-]{0,127}$/;

function microsoftIdp(m: LoginMethod): MicrosoftPlan['idp'] | null {
	const s = (k: string) => (typeof m.meta[k] === 'string' ? (m.meta[k] as string).trim() : '');
	const clientId = s('client_id');
	const redirectUri = s('redirect_uri');
	const tenantId = s('tenant_id');
	let redirectOk = false;
	try {
		const u = new URL(redirectUri);
		redirectOk = u.protocol === 'https:' || u.protocol === 'http:';
	} catch {
		/* not a URL */
	}
	if (!clientId || !redirectOk || !TENANT_ID.test(tenantId)) return null;
	return {
		clientId,
		redirectUri,
		tenantId,
		domainHint: s('domain_hint') || null,
		countryCode: s('country_code') || 'IN'
	};
}

/**
 * Starts a sign-in: checks the method is active in ISFC, generates PKCE (S256), `state` and
 * `nonce`, and returns what the route must do. The route stores `loginState` sealed in
 * `hub_login`. Email OTP: redirect to Hydra. Microsoft: a plan for the browser (it needs Hydra's
 * CSRF cookie in the browser, see `$lib/auth/microsoft`).
 */
export async function startLogin(
	config: IsfcConfig,
	fetchImpl: Fetch,
	input: StartInput,
	now = Date.now()
): Promise<StartResult> {
	if (input.method !== METHOD_EMAIL_OTP && input.method !== METHOD_MICROSOFT) {
		return { ok: false, status: 400, message: 'Choose a sign-in method.' };
	}
	const methodId: ConsoleMethodId = input.method;
	let methods: LoginMethod[];
	try {
		methods = await fetchLoginMethods(config, fetchImpl);
	} catch {
		return { ok: false, status: 503, message: 'ISFC did not answer. Try again in a moment.' };
	}
	const method = methods.find((m) => m.id === methodId);
	if (!method) {
		return { ok: false, status: 400, message: 'This sign-in method is not available.' };
	}
	const pkce = generatePkce();
	const loginState: LoginState = {
		v: 1,
		state: randomToken(),
		nonce: randomToken(),
		verifier: pkce.verifier,
		method: methodId,
		next: safeNext(input.next),
		iat: Math.floor(now / 1000)
	};
	const redirectUri = redirectUriFor(config, input.origin);
	const url = authorizeUrl(config, {
		redirectUri,
		state: loginState.state,
		nonce: loginState.nonce,
		challenge: pkce.challenge
	});
	if (methodId === METHOD_EMAIL_OTP) return { ok: true, kind: 'redirect', url, loginState };

	const idp = microsoftIdp(method);
	if (!idp) {
		return {
			ok: false,
			status: 503,
			message:
				'Microsoft sign-in is not fully set up in ISFC (client id, redirect or tenant id is missing).'
		};
	}
	return {
		ok: true,
		kind: 'microsoft',
		loginState,
		plan: {
			flowUrl: url,
			deviceId: input.deviceId,
			clientId: config.clientId,
			consoleRedirectUri: redirectUri,
			idp
		}
	};
}

// --------------------------------------------------------------------------------- callback

export interface CallbackInput {
	/** Query of `/auth/callback`. */
	code: string | null;
	state: string | null;
	error: string | null;
	errorCode: string | null;
	/** The sealed `hub_login` cookie value. */
	loginCookie: string | undefined;
	origin: string;
	deviceId: string;
	userAgent: string | null;
	secret: string;
}

export type CallbackResult =
	{ ok: true; session: Session; next: string } | { ok: false; code: string };

function sameString(a: string, b: string): boolean {
	const x = Buffer.from(a);
	const y = Buffer.from(b);
	return x.length === y.length && timingSafeEqual(x, y);
}

/** The user shown before `/me` answers. Roles and tenants come from `/me`, never from the token. */
function userFromToken(accessToken: string): SessionUser | null {
	const c = jwtClaims(accessToken);
	const sub = typeof c.sub === 'string' ? c.sub : null;
	if (!sub) return null;
	const email =
		typeof c.email === 'string'
			? c.email
			: typeof c['x-isfc-sub-email-id'] === 'string'
				? c['x-isfc-sub-email-id']
				: null;
	return {
		subject: sub,
		name: typeof c.name === 'string' ? c.name : null,
		email,
		roles: [],
		tenants: []
	};
}

/**
 * Finishes a sign-in at `/auth/callback`: the redirect's `state` must match the one in
 * `hub_login` (constant time), the code is then exchanged server-side. Any failure returns an
 * error code the login page can show; nothing from the query string is echoed.
 */
export async function completeLogin(
	config: IsfcConfig,
	fetchImpl: Fetch,
	input: CallbackInput,
	now = Date.now()
): Promise<CallbackResult> {
	const login = openLoginState(input.loginCookie, input.secret, now);
	if (!login) return { ok: false, code: 'login_expired' };
	if (input.error || input.errorCode) {
		const code = [input.errorCode, input.error].find(isKnownErrorCode);
		return { ok: false, code: code ?? 'access_denied' };
	}
	if (!input.state || !sameString(input.state, login.state)) {
		return { ok: false, code: 'state_mismatch' };
	}
	if (!input.code) return { ok: false, code: 'sign_in_failed' };

	let tokens: TokenSet;
	try {
		tokens = await exchangeCode(
			config,
			fetchImpl,
			{
				code: input.code,
				verifier: login.verifier,
				redirectUri: redirectUriFor(config, input.origin),
				method: login.method,
				deviceId: input.deviceId,
				userAgent: input.userAgent
			},
			now
		);
	} catch (e) {
		if (e instanceof IsfcError) {
			return { ok: false, code: e.transient ? 'unavailable' : knownErrorCode(e.code) };
		}
		return { ok: false, code: 'sign_in_failed' };
	}
	// The nonce is only echoed when ISFC returns an id_token; when it does, it must be ours.
	if (tokens.idTokenNonce !== null && !sameString(tokens.idTokenNonce, login.nonce)) {
		return { ok: false, code: 'state_mismatch' };
	}
	const user = userFromToken(tokens.accessToken);
	if (!user) return { ok: false, code: 'sign_in_failed' };
	return {
		ok: true,
		next: safeNext(login.next),
		session: newSession(
			{
				provider: 'isfc',
				user,
				accessToken: tokens.accessToken,
				refreshToken: tokens.refreshToken,
				isfcSessionId: tokens.sessionId,
				accessExp: tokens.accessExp,
				deviceId: input.deviceId
			},
			now
		)
	};
}

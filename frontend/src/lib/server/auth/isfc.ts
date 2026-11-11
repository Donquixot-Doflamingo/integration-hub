import { createHash, randomBytes } from 'node:crypto';
import type { IsfcConfig } from './config';

/**
 * Requests to ISFC (auth host and gateway), shaped like ISFC's own `omniauth` client
 * (`Isfc/isfc-local/isfc/omniauth/src/client.ts`) and the api-gateway token handler. Everything
 * takes `fetch` as a parameter: tests answer with a fake auth-service and never call a real ISFC
 * or Entra (AUTH_PLAN §18). Tokens only ever pass through server code.
 */
export type Fetch = typeof fetch;

/** Login methods the console offers; every other ISFC method is ignored (AUTH_PLAN §9). */
export const METHOD_EMAIL_OTP = 'EMAIL_OTP';
export const METHOD_MICROSOFT = 'MICROSOFT_SSO';
export const CONSOLE_METHODS = [METHOD_EMAIL_OTP, METHOD_MICROSOFT] as const;
export type ConsoleMethodId = (typeof CONSOLE_METHODS)[number];

export interface LoginMethod {
	id: ConsoleMethodId;
	name: string;
	/** ISFC's `meta_data` of the method (Microsoft: client_id, redirect_uri, tenant_id, ...). */
	meta: Record<string, unknown>;
}

/** An ISFC answer that was not 2xx (or not the expected shape). `code` is ISFC's `error_code`. */
export class IsfcError extends Error {
	constructor(
		readonly status: number,
		readonly code: string,
		message: string
	) {
		super(message);
		this.name = 'IsfcError';
	}
	/** True when ISFC could not be reached or answered 5xx: the session may still be fine. */
	get transient(): boolean {
		return this.status === 0 || this.status >= 500;
	}
}

interface ErrorBody {
	error_code?: unknown;
	error?: unknown;
	message?: unknown;
}

async function parse(res: Response): Promise<unknown> {
	const text = await res.text();
	try {
		return text ? JSON.parse(text) : undefined;
	} catch {
		return { error: text.slice(0, 200) };
	}
}

/** One ISFC call. Throws `IsfcError` (status 0 on a network failure). Never logs bodies. */
export async function isfcCall<T>(
	fetchImpl: Fetch,
	url: string,
	init: {
		method: 'GET' | 'POST';
		headers?: Record<string, string>;
		body?: unknown;
		deviceId?: string;
		userAgent?: string | null;
	}
): Promise<{ status: number; body: T }> {
	const headers: Record<string, string> = { accept: 'application/json', ...init.headers };
	if (init.deviceId) headers['x-device-id'] = init.deviceId;
	if (init.userAgent) headers['user-agent'] = init.userAgent;
	if (init.body !== undefined) headers['content-type'] = 'application/json';
	let res: Response;
	try {
		res = await fetchImpl(url, {
			method: init.method,
			headers,
			body: init.body === undefined ? undefined : JSON.stringify(init.body),
			redirect: 'manual'
		});
	} catch {
		throw new IsfcError(0, 'unavailable', 'ISFC did not answer.');
	}
	const body = (await parse(res)) as T & ErrorBody;
	if (!res.ok) {
		const e = (body ?? {}) as ErrorBody;
		const code = typeof e.error_code === 'string' ? e.error_code : '';
		const message =
			typeof e.message === 'string' ? e.message : typeof e.error === 'string' ? e.error : '';
		throw new IsfcError(res.status, code, message || `ISFC answered ${res.status}.`);
	}
	return { status: res.status, body: body as T };
}

// ---------------------------------------------------------------------------------- PKCE

const b64url = (b: Buffer) => b.toString('base64url');

/** RFC 7636 S256 pair: a 64-byte random verifier (86 characters) and its SHA-256 challenge. */
export function generatePkce(): { verifier: string; challenge: string } {
	const verifier = b64url(randomBytes(64));
	return { verifier, challenge: b64url(createHash('sha256').update(verifier).digest()) };
}

/** Unguessable value for `state` and `nonce` (16 random bytes, hex, like omniauth). */
export function randomToken(): string {
	return randomBytes(16).toString('hex');
}

// ------------------------------------------------------------------------------ login methods

/** `GET {auth}/oauth2/method/<client>`, reduced to the two methods the console offers. */
export async function fetchLoginMethods(
	config: IsfcConfig,
	fetchImpl: Fetch
): Promise<LoginMethod[]> {
	const { body } = await isfcCall<{
		data?: { id?: unknown; name?: unknown; meta_data?: unknown }[] | null;
	}>(fetchImpl, `${config.authUrl}/oauth2/method/${encodeURIComponent(config.clientId)}`, {
		method: 'GET'
	});
	const out: LoginMethod[] = [];
	for (const m of body?.data ?? []) {
		const id = CONSOLE_METHODS.find((c) => c === m.id);
		if (!id || out.some((o) => o.id === id)) continue;
		const meta =
			typeof m.meta_data === 'object' && m.meta_data !== null
				? (m.meta_data as Record<string, unknown>)
				: {};
		out.push({ id, name: typeof m.name === 'string' && m.name ? m.name : defaultName(id), meta });
	}
	// Stable order: Email OTP first.
	return out.sort((a, b) => CONSOLE_METHODS.indexOf(a.id) - CONSOLE_METHODS.indexOf(b.id));
}

function defaultName(id: ConsoleMethodId): string {
	return id === METHOD_EMAIL_OTP ? 'Email OTP' : 'Microsoft';
}

/** Labels the login page shows, whatever ISFC calls the method. */
export const METHOD_LABELS: Record<ConsoleMethodId, string> = {
	EMAIL_OTP: 'Email OTP',
	MICROSOFT_SSO: 'Microsoft'
};

// ------------------------------------------------------------------------------ authorize URL

export interface AuthorizeParams {
	redirectUri: string;
	state: string;
	nonce: string;
	challenge: string;
}

/**
 * Hydra's authorization request (through the shared auth host). Email OTP sends the browser
 * here; Microsoft fetches it with `credentials: include` to get the flow token (omniauth `sso()`).
 */
export function authorizeUrl(config: IsfcConfig, p: AuthorizeParams): string {
	const q = new URLSearchParams({
		response_type: 'code',
		client_id: config.clientId,
		redirect_uri: p.redirectUri,
		scope: 'offline_access',
		state: p.state,
		nonce: p.nonce,
		code_challenge: p.challenge,
		code_challenge_method: 'S256'
	});
	return `${config.authUrl}/oauth2/auth?${q}`;
}

// ---------------------------------------------------------------------------------- tokens

export interface TokenSet {
	accessToken: string;
	refreshToken: string | null;
	sessionId: string | null;
	/** Access token expiry, epoch seconds. */
	accessExp: number;
	/** `nonce` claim of an `id_token`, when ISFC returned one. */
	idTokenNonce: string | null;
}

interface TokenResponse {
	data?: {
		access_token?: unknown;
		refresh_token?: unknown;
		session_id?: unknown;
		expires_in?: unknown;
		id_token?: unknown;
	};
}

/** Claims of a JWT, read without verifying: for display and expiry only (the gateway verifies). */
export function jwtClaims(jwt: string): Record<string, unknown> {
	const part = jwt.split('.')[1];
	if (!part) return {};
	try {
		const v = JSON.parse(Buffer.from(part, 'base64url').toString('utf8'));
		return typeof v === 'object' && v !== null ? (v as Record<string, unknown>) : {};
	} catch {
		return {};
	}
}

const str = (v: unknown): string | null => (typeof v === 'string' && v ? v : null);

function toTokenSet(body: TokenResponse | undefined, now: number): TokenSet {
	const d = body?.data;
	const accessToken = str(d?.access_token);
	if (!accessToken) throw new IsfcError(502, 'unexpected_response', 'ISFC sent no access token.');
	const claims = jwtClaims(accessToken);
	const expiresIn = typeof d?.expires_in === 'number' ? d.expires_in : null;
	const accessExp =
		expiresIn !== null
			? Math.floor(now / 1000) + expiresIn
			: typeof claims.exp === 'number'
				? claims.exp
				: Math.floor(now / 1000) + 300;
	const idToken = str(d?.id_token);
	return {
		accessToken,
		refreshToken: str(d?.refresh_token),
		sessionId: str(d?.session_id),
		accessExp,
		idTokenNonce: idToken ? str(jwtClaims(idToken).nonce) : null
	};
}

export interface ExchangeInput {
	code: string;
	verifier: string;
	redirectUri: string;
	/** Login method that was used (`EMAIL_OTP`, `MICROSOFT_SSO`), recorded in the session info. */
	method: string;
	deviceId: string;
	/** The browser's User-Agent: auth-service binds the session to device and agent. */
	userAgent: string | null;
}

/**
 * Server-side code exchange: `POST {auth}/oauth2/token` with `x-device-id` and the browser's
 * User-Agent (AUTH_PLAN §9). The code and verifier are used once and never stored.
 */
export async function exchangeCode(
	config: IsfcConfig,
	fetchImpl: Fetch,
	input: ExchangeInput,
	now = Date.now()
): Promise<TokenSet> {
	const { body } = await isfcCall<TokenResponse>(fetchImpl, `${config.authUrl}/oauth2/token`, {
		method: 'POST',
		deviceId: input.deviceId,
		userAgent: input.userAgent,
		body: {
			grant_type: 'authorization_code',
			client_id: config.clientId,
			redirect_uri: input.redirectUri,
			code: input.code,
			code_verifier: input.verifier,
			session_info: {
				platform: 'WEB',
				login_method: input.method,
				device_info: { device_id: input.deviceId }
			}
		}
	});
	return toTokenSet(body, now);
}

/** Wait before the single retry of a 409 (another request is rotating the same token). */
export const REFRESH_RETRY_MS = 300;

export interface RefreshInput {
	refreshToken: string;
	sessionId: string;
	redirectUri: string;
	deviceId: string;
	userAgent: string | null;
}

/**
 * `refresh_token` grant. A 409 means a concurrent refresh is rotating the same token; after
 * 300 ms (like the gateway's token handler) the repeat is answered from auth-service's replay
 * cache. Only one retry.
 */
export async function refreshTokens(
	config: IsfcConfig,
	fetchImpl: Fetch,
	input: RefreshInput,
	opts: { now?: number; sleep?: (ms: number) => Promise<void> } = {}
): Promise<TokenSet> {
	const sleep = opts.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
	const call = () =>
		isfcCall<TokenResponse>(fetchImpl, `${config.authUrl}/oauth2/token`, {
			method: 'POST',
			deviceId: input.deviceId,
			userAgent: input.userAgent,
			body: {
				grant_type: 'refresh_token',
				client_id: config.clientId,
				redirect_uri: input.redirectUri,
				refresh_token: input.refreshToken,
				session_id: input.sessionId,
				session_info: { platform: 'WEB', login_method: 'refresh' }
			}
		});
	try {
		return toTokenSet((await call()).body, opts.now ?? Date.now());
	} catch (e) {
		if (!(e instanceof IsfcError) || e.status !== 409) throw e;
		await sleep(REFRESH_RETRY_MS);
		return toTokenSet((await call()).body, opts.now ?? Date.now());
	}
}

/** `POST {auth}/api/v1/logout { session_id }` with the access token. Best effort for the caller. */
export async function logoutSession(
	config: IsfcConfig,
	fetchImpl: Fetch,
	input: { accessToken: string; sessionId: string; deviceId: string; userAgent: string | null }
): Promise<void> {
	await isfcCall(fetchImpl, `${config.authUrl}/api/v1/logout`, {
		method: 'POST',
		headers: { authorization: `Bearer ${input.accessToken}` },
		deviceId: input.deviceId,
		userAgent: input.userAgent,
		body: { session_id: input.sessionId }
	});
}

/**
 * Microsoft SSO, browser half (HUB-493, AUTH_PLAN §9). The server issues the PKCE challenge and
 * `state` (the verifier never leaves the server) and hands the browser this plan. The browser
 * only does what ISFC's `omniauth` `sso()` does and what cannot be done on the server because
 * Hydra's login CSRF cookie must land in the browser:
 *   1. GET the Hydra authorization URL with `credentials: include` -> `flow_token`;
 *   2. redirect to Entra with ISFC's `state` blob (method, flow token, device, redirect);
 *   3. Entra returns to auth-service, which finishes the flow; Hydra redirects to
 *      `/auth/callback?code=&state=`, where the server checks `state` and exchanges the code.
 * No token passes through this code. It runs against a fake provider in unit tests.
 */

export const ENTRA_AUTHORITY = 'https://login.microsoftonline.com';
const MICROSOFT_SCOPE = 'openid email profile User.Read';

export interface MicrosoftPlan {
	/** Hydra authorization URL (with the server's PKCE challenge and state) that opens the flow. */
	flowUrl: string;
	/** Stable per-browser id; auth-service binds the session to it. */
	deviceId: string;
	/** The console's client id in ISFC. */
	clientId: string;
	/** The console's registered redirect URI (`/auth/callback`). */
	consoleRedirectUri: string;
	/** From the login method's `meta_data` in ISFC. */
	idp: {
		clientId: string;
		/** auth-service's `/oauth2/callback`. */
		redirectUri: string;
		/** Entra tenant id. */
		tenantId: string;
		domainHint: string | null;
		countryCode: string;
	};
}

/** base64url(JSON), the `state` blob auth-service's `/oauth2/callback` understands. */
export function encodeState(obj: Record<string, unknown>): string {
	const bytes = new TextEncoder().encode(JSON.stringify(obj));
	let bin = '';
	bytes.forEach((b) => (bin += String.fromCharCode(b)));
	return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/** Entra's authorize URL for the plan and the flow token. */
export function microsoftAuthorizeUrl(
	plan: MicrosoftPlan,
	flowToken: string,
	authority: string = ENTRA_AUTHORITY
): string {
	const state = encodeState({
		method_id: 'MICROSOFT_SSO',
		flow_token: flowToken,
		device_id: plan.deviceId,
		country_code: plan.idp.countryCode,
		client_id: plan.clientId,
		redirect_uri: plan.consoleRedirectUri,
		client_redirect_uri: plan.consoleRedirectUri
	});
	const q = new URLSearchParams({
		client_id: plan.idp.clientId,
		response_type: 'code',
		response_mode: 'query',
		redirect_uri: plan.idp.redirectUri,
		scope: MICROSOFT_SCOPE,
		state
	});
	if (plan.idp.domainHint) q.set('domain_hint', plan.idp.domainHint);
	return `${authority}/${encodeURIComponent(plan.idp.tenantId)}/oauth2/v2.0/authorize?${q}`;
}

export class MicrosoftSsoError extends Error {
	override readonly name = 'MicrosoftSsoError';
}

/**
 * Opens the Hydra flow and sends the browser to Entra. Throws `MicrosoftSsoError` with a message
 * fit for the page when ISFC does not answer or sends no flow token.
 */
export async function startMicrosoftSso(
	plan: MicrosoftPlan,
	deps: {
		fetch: typeof fetch;
		assign: (url: string) => void;
		authority?: string;
	}
): Promise<void> {
	let res: Response;
	try {
		res = await deps.fetch(plan.flowUrl, {
			method: 'GET',
			credentials: 'include',
			headers: { Accept: 'application/json', 'x-device-id': plan.deviceId }
		});
	} catch {
		throw new MicrosoftSsoError('ISFC did not answer. Try again in a moment.');
	}
	let flowToken: unknown;
	try {
		const body = (await res.json()) as { data?: { flow_token?: unknown } };
		flowToken = body?.data?.flow_token;
	} catch {
		flowToken = undefined;
	}
	if (!res.ok || typeof flowToken !== 'string' || !flowToken) {
		throw new MicrosoftSsoError('ISFC did not start the Microsoft sign-in. Try again.');
	}
	deps.assign(microsoftAuthorizeUrl(plan, flowToken, deps.authority));
}

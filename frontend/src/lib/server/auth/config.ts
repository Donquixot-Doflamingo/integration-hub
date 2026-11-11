import { env as privateEnv } from '$env/dynamic/private';

/**
 * ISFC connection settings (private env, server only; values are never sent to the browser or
 * logged). The "auth host" is the shared ISFC host that routes `/oauth2/auth` to Hydra and
 * `/oauth2/*` + `/api/v1/*` to auth-service (AUTH_PLAN §9).
 */
export const ISFC_ENV = {
	authUrl: 'HUB_ISFC_AUTH_URL',
	clientId: 'HUB_ISFC_CLIENT_ID',
	redirectUri: 'HUB_ISFC_REDIRECT_URI',
	gatewayUrl: 'HUB_ISFC_GATEWAY_URL',
	platformTenantId: 'HUB_ISFC_PLATFORM_TENANT_ID'
} as const;

/** The console client in ISFC (AUTH_PLAN §2.4). */
export const DEFAULT_CONSOLE_CLIENT_ID = 'integration-hub-console';

export interface IsfcConfig {
	/** Base URL of the shared ISFC auth host, no trailing slash. */
	authUrl: string;
	/** The console's OAuth client id in ISFC. */
	clientId: string;
	/** Registered redirect URI; null means `<origin>/auth/callback` of the request. */
	redirectUri: string | null;
	/** Base URL of the ISFC api-gateway (`/auth/oauth2/panels`); null disables the tenant switcher. */
	gatewayUrl: string | null;
	/** ISFC UUID of the platform tenant, listed first in the tenant switcher. */
	platformTenantId: string | null;
}

type Env = Record<string, string | undefined>;

function httpUrl(raw: string | undefined): string | null {
	const v = raw?.trim();
	if (!v) return null;
	try {
		const u = new URL(v);
		if (u.protocol !== 'https:' && u.protocol !== 'http:') return null;
		return v.replace(/\/+$/, '');
	} catch {
		return null;
	}
}

/** The settings, or the names of the ones that are missing or malformed. */
export function readIsfcConfig(
	env: Env = privateEnv
): { ok: true; config: IsfcConfig } | { ok: false; missing: string[] } {
	const authUrl = httpUrl(env[ISFC_ENV.authUrl]);
	if (!authUrl) return { ok: false, missing: [ISFC_ENV.authUrl] };
	return {
		ok: true,
		config: {
			authUrl,
			clientId: env[ISFC_ENV.clientId]?.trim() || DEFAULT_CONSOLE_CLIENT_ID,
			redirectUri: httpUrl(env[ISFC_ENV.redirectUri]),
			gatewayUrl: httpUrl(env[ISFC_ENV.gatewayUrl]),
			platformTenantId: env[ISFC_ENV.platformTenantId]?.trim().toLowerCase() || null
		}
	};
}

/** The redirect URI registered for the console: the setting, else this origin's `/auth/callback`. */
export function redirectUriFor(config: IsfcConfig, origin: string): string {
	return config.redirectUri ?? `${origin}/auth/callback`;
}

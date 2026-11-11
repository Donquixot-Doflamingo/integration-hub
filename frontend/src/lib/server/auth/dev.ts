import type { Role } from '$lib/api/types';
import { isRole } from '$lib/auth/roles';
import type { AuthProvider, SignInResult } from './provider';
import { SESSION_TTL_SECONDS } from './session';

/** Contract `Slug` pattern (hub-admin.v1.yaml `components.schemas.Slug`). */
const SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/;

function text(form: FormData, key: string): string {
	const v = form.get(key);
	return typeof v === 'string' ? v.trim() : '';
}

/** Options of the dev provider. */
export interface DevProviderOptions {
	/**
	 * Base URL of the Hub admin's dev OIDC issuer (`hub admin -admin-dev-oidc`, M7), from
	 * `HUB_DEV_OIDC_ISSUER`. When set, sign-in asks the issuer (discovery, then its token
	 * endpoint) for an access token with the chosen identity, so the console works against a real
	 * admin API (HUB-286). Unset (mock mode): sessions carry no token.
	 */
	issuer?: string;
	/** Injected in tests. */
	fetch?: typeof fetch;
}

/** Body of the dev issuer's token endpoint (backend `adminapi.DevIssuer`; not part of `/admin/v1`). */
interface DevTokenRequest {
	sub: string;
	email: string;
	name: string;
	roles: Role[];
	tenants: string[];
	ttl_s: number;
}

type Minted = { ok: true; token: string } | { ok: false; message: string };

/**
 * Discovery document, then `token_endpoint`. The token goes back to the caller only (into the
 * httpOnly session) and is never logged or sent to the browser.
 */
export async function mintDevToken(
	issuer: string,
	request: DevTokenRequest,
	fetchImpl: typeof fetch
): Promise<Minted> {
	const base = issuer.replace(/\/+$/, '');
	const unreachable = `The Hub admin dev issuer at ${base} did not answer. Is the admin role running with -admin-dev-oidc?`;
	try {
		const disco = await fetchImpl(`${base}/.well-known/openid-configuration`);
		if (!disco.ok) return { ok: false, message: unreachable };
		const { token_endpoint } = (await disco.json()) as { token_endpoint?: unknown };
		if (typeof token_endpoint !== 'string') return { ok: false, message: unreachable };
		const res = await fetchImpl(token_endpoint, {
			method: 'POST',
			headers: { 'content-type': 'application/json' },
			body: JSON.stringify(request)
		});
		const out = (await res.json().catch(() => null)) as { access_token?: unknown } | null;
		if (!res.ok || typeof out?.access_token !== 'string') {
			return { ok: false, message: `The dev issuer refused the sign-in (HTTP ${res.status}).` };
		}
		return { ok: true, token: out.access_token };
	} catch {
		return { ok: false, message: unreachable };
	}
}

/** Validates the role picker form; the identity, or the field that is wrong. */
function readIdentity(
	form: FormData
):
	| { ok: true; role: Role; name: string; email: string | null; tenants: string[] }
	| Extract<SignInResult, { ok: false }> {
	const role = text(form, 'role');
	if (!isRole(role)) {
		return { ok: false, status: 400, field: 'role', message: 'Choose a role.' };
	}
	const name = text(form, 'name') || 'Dev operator';
	const email = text(form, 'email') || null;
	if (email && !/^[^\s@]+@[^\s@]+$/.test(email)) {
		return {
			ok: false,
			status: 400,
			field: 'email',
			message: 'Enter an email like name@example.com.'
		};
	}
	let tenants: string[] = [];
	if (role === 'tenant_viewer' || role === 'tenant_admin') {
		tenants = text(form, 'tenants')
			.split(/[\s,]+/)
			.filter(Boolean);
		if (tenants.length === 0) {
			return {
				ok: false,
				status: 400,
				field: 'tenants',
				message: 'A tenant viewer needs at least one tenant, for example orders-service.'
			};
		}
		const bad = tenants.find((t) => !SLUG.test(t));
		if (bad) {
			return {
				ok: false,
				status: 400,
				field: 'tenants',
				message: `"${bad}" is not a tenant id (3 to 64 letters, digits, - or _).`
			};
		}
	}
	return { ok: true, role, name, email, tenants };
}

/**
 * Dev/mock sign-in: the operator picks a role (and tenants for the tenant roles); no password.
 * In mock mode there is no identity provider and no token. Against a running Hub, the admin's
 * dev issuer mints the token (`issuer`). Only active in mock builds or with
 * `PUBLIC_AUTH_MODE=dev` (see `authProvider()`).
 */
export function createDevProvider(opts: DevProviderOptions = {}): AuthProvider {
	const issuer = opts.issuer?.trim() || null;
	return {
		id: 'dev',
		describe: () => ({
			provider: 'dev',
			ready: true,
			notice: issuer
				? 'Development sign-in: choose a role. The Hub admin dev issuer signs your token; no password.'
				: 'Development sign-in: choose a role. No identity provider is involved.',
			missing: []
		}),
		async signIn(form) {
			const id = readIdentity(form);
			if (!id.ok) return id;
			const { role, name, email, tenants } = id;
			const roles: Role[] = [role];
			const subject = `dev|${email ?? name}`;
			let accessToken: string | null = null;
			if (issuer) {
				const minted = await mintDevToken(
					issuer,
					{ sub: subject, email: email ?? '', name, roles, tenants, ttl_s: SESSION_TTL_SECONDS },
					opts.fetch ?? fetch
				);
				if (!minted.ok) return { ok: false, status: 503, message: minted.message };
				accessToken = minted.token;
			}
			return {
				ok: true,
				session: { provider: 'dev', user: { subject, name, email, roles, tenants }, accessToken }
			};
		}
	};
}

/** The dev provider without an issuer (mock mode). */
export const devProvider: AuthProvider = createDevProvider();

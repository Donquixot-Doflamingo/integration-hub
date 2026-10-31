import type { Role } from '$lib/api/types';
import { isRole } from '$lib/auth/roles';
import type { AuthProvider } from './provider';

/** Contract `Slug` pattern (hub-admin.v1.yaml `components.schemas.Slug`). */
const SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/;

function text(form: FormData, key: string): string {
	const v = form.get(key);
	return typeof v === 'string' ? v.trim() : '';
}

/**
 * Dev/mock sign-in: the operator picks a role (and tenants for `tenant_viewer`). No identity
 * provider and no access token; against a real admin API this needs the backend's dev auth.
 * Only active in mock builds or with `PUBLIC_AUTH_MODE=dev` (see `authProvider()`).
 */
export const devProvider: AuthProvider = {
	id: 'dev',
	describe: () => ({
		provider: 'dev',
		ready: true,
		notice: 'Development sign-in: choose a role. No identity provider is involved.',
		missing: []
	}),
	async signIn(form) {
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
		if (role === 'tenant_viewer') {
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
		const roles: Role[] = [role];
		return {
			ok: true,
			session: {
				provider: 'dev',
				user: { subject: `dev|${email ?? name}`, name, email, roles, tenants },
				accessToken: null
			}
		};
	}
};

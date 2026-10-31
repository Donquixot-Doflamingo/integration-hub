import type { ApiResult } from '$lib/api/client';
import type { Me, Role } from '$lib/api/types';

/**
 * Role model from hub-admin.v1.yaml: `admin` ⊃ `editor` ⊃ `viewer`; `tenant_viewer` is a viewer
 * restricted to the tenants in its token. Each operation declares its minimum role in
 * `x-required-role`. The UI hides or disables what a role cannot do; the API's 403 stays the truth.
 */
export const ROLES: readonly Role[] = ['admin', 'editor', 'viewer', 'tenant_viewer'];

const RANK: Record<Role, number> = { tenant_viewer: 0, viewer: 1, editor: 2, admin: 3 };

export const roleLabels: Record<Role, string> = {
	admin: 'Admin',
	editor: 'Editor',
	viewer: 'Viewer',
	tenant_viewer: 'Tenant viewer'
};

/** What each role may do, in the operator's words (login picker, user menu). */
export const roleSummaries: Record<Role, string> = {
	admin: 'Everything, including tenants, API keys and breaker force.',
	editor: 'Publish, roll back, set weights, pause and replay dead letters.',
	viewer: 'Read everything. No changes.',
	tenant_viewer: 'Read only, limited to your own tenants.'
};

export function isRole(value: unknown): value is Role {
	return typeof value === 'string' && (ROLES as readonly string[]).includes(value);
}

/** True when any of `roles` is at least `min` in the hierarchy. */
export function hasRole(roles: readonly Role[], min: Role): boolean {
	return roles.some((r) => RANK[r] >= RANK[min]);
}

/** The highest role held, or null for none. */
export function highestRole(roles: readonly Role[]): Role | null {
	return roles.reduce<Role | null>(
		(best, r) => (best === null || RANK[r] > RANK[best] ? r : best),
		null
	);
}

/** What the signed-in user may see and do, derived from `/me`. Fails closed when `/me` failed. */
export interface Access {
	/** True when `/me` answered; false means every gated control stays hidden. */
	known: boolean;
	roles: Role[];
	/** Tenant scope (`tenant_viewer`); empty means all tenants. */
	tenants: string[];
	/** Highest role, for labels. */
	role: Role | null;
	/** Can run mutations (publish, rollback, weights, pause, replay): `editor` or above. */
	canMutate: boolean;
	/** Can run admin-only mutations (tenants, API keys, breaker force). */
	isAdmin: boolean;
	/** Only `tenant_viewer`: results are filtered to `tenants`, no vendor/account filters. */
	tenantScoped: boolean;
}

export function accessFromRoles(roles: readonly Role[], tenants: readonly string[] = []): Access {
	const role = highestRole(roles);
	return {
		known: true,
		roles: [...roles],
		tenants: [...tenants],
		role,
		canMutate: hasRole(roles, 'editor'),
		isAdmin: hasRole(roles, 'admin'),
		tenantScoped: role === 'tenant_viewer'
	};
}

export const NO_ACCESS: Access = {
	known: false,
	roles: [],
	tenants: [],
	role: null,
	canMutate: false,
	isAdmin: false,
	tenantScoped: true
};

export function accessFromMe(me: ApiResult<Me>): Access {
	return me.ok ? accessFromRoles(me.data.roles, me.data.tenants) : NO_ACCESS;
}

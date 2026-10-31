import { describe, expect, it } from 'vitest';
import type { Role } from '$lib/api/types';
import { accessFromMe, accessFromRoles, hasRole, highestRole, NO_ACCESS } from './roles';
import { HubApiError } from '$lib/api/errors';

// HUB-253: role guard per role. Hierarchy from hub-admin.v1.yaml: admin ⊃ editor ⊃ viewer ⊃ tenant_viewer.

const MINS: Role[] = ['tenant_viewer', 'viewer', 'editor', 'admin'];

describe('hasRole', () => {
	it.each<{ role: Role; allowed: Role[] }>([
		{ role: 'admin', allowed: ['tenant_viewer', 'viewer', 'editor', 'admin'] },
		{ role: 'editor', allowed: ['tenant_viewer', 'viewer', 'editor'] },
		{ role: 'viewer', allowed: ['tenant_viewer', 'viewer'] },
		{ role: 'tenant_viewer', allowed: ['tenant_viewer'] }
	])('$role meets $allowed', ({ role, allowed }) => {
		for (const min of MINS)
			expect(hasRole([role], min), `${role} >= ${min}`).toBe(allowed.includes(min));
	});

	it('no roles meets nothing', () => {
		for (const min of MINS) expect(hasRole([], min)).toBe(false);
	});

	it('uses the strongest of several roles', () => {
		expect(hasRole(['tenant_viewer', 'editor'], 'editor')).toBe(true);
		expect(highestRole(['viewer', 'admin', 'tenant_viewer'])).toBe('admin');
		expect(highestRole([])).toBeNull();
	});
});

describe('accessFromRoles (mutation guard)', () => {
	it.each<{ role: Role; canMutate: boolean; isAdmin: boolean; tenantScoped: boolean }>([
		{ role: 'admin', canMutate: true, isAdmin: true, tenantScoped: false },
		{ role: 'editor', canMutate: true, isAdmin: false, tenantScoped: false },
		{ role: 'viewer', canMutate: false, isAdmin: false, tenantScoped: false },
		{ role: 'tenant_viewer', canMutate: false, isAdmin: false, tenantScoped: true }
	])('$role -> canMutate $canMutate', ({ role, canMutate, isAdmin, tenantScoped }) => {
		const a = accessFromRoles([role], role === 'tenant_viewer' ? ['orders-service'] : []);
		expect(a).toMatchObject({ known: true, role, canMutate, isAdmin, tenantScoped });
	});
});

describe('accessFromMe', () => {
	it('fails closed when /me failed', () => {
		const error = new HubApiError({ status: 500, code: 'INTERNAL_ERROR', message: 'boom' });
		expect(accessFromMe({ ok: false, error, status: 500, requestId: null })).toBe(NO_ACCESS);
		expect(NO_ACCESS.canMutate).toBe(false);
	});

	it('reads roles and tenants from /me', () => {
		const a = accessFromMe({
			ok: true,
			status: 200,
			requestId: null,
			data: { subject: 's', roles: ['tenant_viewer'], tenants: ['orders-service'], permissions: [] }
		});
		expect(a).toMatchObject({ known: true, tenants: ['orders-service'], tenantScoped: true });
	});
});

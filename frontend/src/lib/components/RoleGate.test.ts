import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { Role } from '$lib/api/types';
import { accessFromRoles, NO_ACCESS, type OperationId } from '$lib/auth/roles';
import RoleGateHarness from './RoleGateHarness.test.svelte';

// HUB-253: mutation controls are hidden for viewer and tenant_viewer (and when /me failed).
describe('RoleGate', () => {
	it.each<{ role: Role; visible: boolean }>([
		{ role: 'admin', visible: true },
		{ role: 'editor', visible: true },
		{ role: 'viewer', visible: false },
		{ role: 'tenant_viewer', visible: false }
	])('editor-level control for $role: visible=$visible', ({ role, visible }) => {
		render(RoleGateHarness, { props: { access: accessFromRoles([role]), min: 'editor' } });
		expect(!!screen.queryByRole('button', { name: 'Publish' })).toBe(visible);
		expect(!!screen.queryByText('Read only: needs the Editor role')).toBe(!visible);
	});

	it('admin-level control is hidden for editors', () => {
		render(RoleGateHarness, { props: { access: accessFromRoles(['editor']), min: 'admin' } });
		expect(screen.queryByRole('button', { name: 'Publish' })).toBeNull();
		expect(screen.getByText('Read only: needs the Admin role')).toBeInTheDocument();
	});

	it('hides everything when the role is unknown', () => {
		render(RoleGateHarness, { props: { access: NO_ACCESS, min: 'tenant_viewer' } });
		expect(screen.queryByRole('button', { name: 'Publish' })).toBeNull();
	});

	// HUB-288: the minimum role comes from the operation's x-required-role (roles.gen.ts).
	it.each<{ op: OperationId; role: Role; visible: boolean; needed: string }>([
		{ op: 'purgeDeadLetters', role: 'admin', visible: true, needed: 'Admin' },
		{ op: 'purgeDeadLetters', role: 'editor', visible: false, needed: 'Admin' },
		{ op: 'replayDeadLetters', role: 'editor', visible: true, needed: 'Editor' },
		{ op: 'replayDeadLetters', role: 'viewer', visible: false, needed: 'Editor' },
		{ op: 'deleteRegistrarEndpoint', role: 'editor', visible: false, needed: 'Tenant admin' },
		{ op: 'deleteRegistrarEndpoint', role: 'viewer', visible: false, needed: 'Tenant admin' },
		{
			op: 'deleteRegistrarEndpoint',
			role: 'tenant_viewer',
			visible: false,
			needed: 'Tenant admin'
		},
		{ op: 'deleteRegistrarEndpoint', role: 'tenant_admin', visible: true, needed: 'Tenant admin' },
		{ op: 'deleteRegistrarEndpoint', role: 'admin', visible: true, needed: 'Tenant admin' },
		{ op: 'purgeDeadLetters', role: 'tenant_admin', visible: false, needed: 'Admin' },
		{ op: 'getMe', role: 'tenant_admin', visible: true, needed: 'Tenant viewer' },
		{ op: 'getMe', role: 'tenant_viewer', visible: true, needed: 'Tenant viewer' }
	])('op=$op for $role: visible=$visible', ({ op, role, visible, needed }) => {
		render(RoleGateHarness, { props: { access: accessFromRoles([role]), op } });
		expect(!!screen.queryByRole('button', { name: 'Publish' })).toBe(visible);
		expect(!!screen.queryByText(`Read only: needs the ${needed} role`)).toBe(!visible);
	});

	it('ops: every operation must be allowed, and the note names the highest role needed', () => {
		render(RoleGateHarness, {
			props: { access: accessFromRoles(['editor']), ops: ['replayDeadLetters', 'purgeDeadLetters'] }
		});
		expect(screen.queryByRole('button', { name: 'Publish' })).toBeNull();
		expect(screen.getByText('Read only: needs the Admin role')).toBeInTheDocument();
	});

	it('op wins over a lower min (a gate cannot be loosened below the contract)', () => {
		render(RoleGateHarness, {
			props: { access: accessFromRoles(['viewer']), op: 'createTenant', min: 'viewer' }
		});
		expect(screen.queryByRole('button', { name: 'Publish' })).toBeNull();
	});
});

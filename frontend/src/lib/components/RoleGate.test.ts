import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { Role } from '$lib/api/types';
import { accessFromRoles, NO_ACCESS } from '$lib/auth/roles';
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
});

// @vitest-environment node
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import YAML from 'yaml';
import { requiredRoles } from '../src/lib/api/roles.gen';
// @ts-expect-error -- plain ESM script without types
import { buildRoles, render } from './extract-roles.mjs';

// HUB-288: the console's role gates come from the contract's x-required-role, never by hand.
const SPEC = join(process.cwd(), '..', 'contracts', 'openapi', 'hub-admin.v1.yaml');
const doc = YAML.parse(readFileSync(SPEC, 'utf8'));

describe('extract-roles', () => {
	it('roles.gen.ts is fresh: one entry per contract operation, same role', () => {
		const fromContract = Object.fromEntries(buildRoles(doc));
		expect(requiredRoles).toEqual(fromContract);
		const file = readFileSync(join(process.cwd(), 'src', 'lib', 'api', 'roles.gen.ts'), 'utf8');
		expect(file).toBe(render(buildRoles(doc), doc.info.version));
	});

	it.each([
		['purgeDeadLetters', 'admin'],
		['deleteRegistrarEndpoint', 'tenant_admin'],
		['updateTenant', 'tenant_admin'],
		['syncTenants', 'admin'],
		['syncTenant', 'admin'],
		['revokeApiKey', 'admin'],
		['unsealRequest', 'admin'],
		['forceOpenBreaker', 'admin'],
		['transitionAccountLifecycle', 'admin'],
		['rollbackIntegration', 'editor'],
		['replayDeadLetters', 'editor'],
		['getIntegration', 'tenant_viewer']
	])('%s needs %s', (op, role) => {
		expect(requiredRoles[op as keyof typeof requiredRoles]).toBe(role);
	});

	it('accepts tenant_admin (contract 2.1.0) and still refuses unknown roles', () => {
		const ok = {
			paths: { '/t': { patch: { operationId: 'setupT', 'x-required-role': 'tenant_admin' } } }
		};
		expect(buildRoles(ok)).toEqual([['setupT', 'tenant_admin']]);
		expect(() =>
			buildRoles({
				paths: { '/t': { get: { operationId: 'x', 'x-required-role': 'tenant_owner' } } }
			})
		).toThrow(/tenant_owner/);
	});

	it('refuses an operation without a valid x-required-role', () => {
		const bad = {
			paths: { '/x': { delete: { operationId: 'deleteX', 'x-required-role': 'root' } } }
		};
		expect(() => buildRoles(bad)).toThrow(/deleteX: x-required-role "root"/);
		expect(() => buildRoles({ paths: { '/y': { get: { 'x-required-role': 'viewer' } } } })).toThrow(
			/GET \/y: missing operationId/
		);
	});
});

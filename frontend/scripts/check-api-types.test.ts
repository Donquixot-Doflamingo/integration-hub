// @vitest-environment node
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
// @ts-expect-error plain ESM script without type declarations
import { findViolations, schemaNamesFrom } from './check-api-types.mjs';

const names: Set<string> = schemaNamesFrom(
	readFileSync(resolve(process.cwd(), 'src/lib/api/schema.d.ts'), 'utf8')
);

describe('check-api-types guard', () => {
	it('reads the contract schema names from schema.d.ts', () => {
		expect(names.has('Integration')).toBe(true);
		expect(names.has('AdminError')).toBe(true);
		expect(names.size).toBeGreaterThan(100);
	});

	it.each([
		{ src: 'interface Integration { id: string }', bad: true },
		{ src: 'export type Tenant = { tenant_id: string };', bad: true },
		{ src: "type Me = components['schemas']['Me'];", bad: true },
		{ src: 'export interface IntegrationRow { id: string; paused: boolean }', bad: true },
		{ src: 'type VendorThing = {', bad: true },
		{ src: "const r = await fetch('/admin/v1/integrations');", bad: true },
		{ src: 'const r = await fetch(`${base}/admin/v1/me`);', bad: true },
		{ src: 'interface IntegrationTableProps { rows: Row[] }', bad: false },
		{ src: "type IntegrationId = Schemas['Slug'];", bad: false },
		{ src: 'interface NavItem { href: string; label: string }', bad: false },
		{
			src: 'interface Integration { id: string } // api-types-guard: allow test double',
			bad: false
		}
	])('$src -> violation: $bad', ({ src, bad }) => {
		expect(findViolations(src, names).length > 0).toBe(bad);
	});
});

#!/usr/bin/env node
// Guard (HUB-251): API types are generated, never hand-written (AGENTS.md §4.6, frontend/AGENTS.md).
//
// Scans src/**/*.{ts,js,svelte} OUTSIDE src/lib/api/ and fails when it finds:
//   1. a type/interface/enum/class whose name equals a contract schema name
//      (`components.schemas.*` read from the generated src/lib/api/schema.d.ts), e.g. `interface Integration`;
//   2. an object-shaped declaration (`interface X {` or `type X = {`) whose name contains a core
//      domain noun (Integration, Account, Vendor, Tenant, ...), except names ending in Props/Options;
//   3. a raw `fetch(...)` to `/admin/v1` (all calls go through src/lib/api/client.ts).
// Put aliases in src/lib/api/types.ts instead. Escape hatch for a genuine false positive: add
// `api-types-guard: allow <why>` in a comment on the same line.
// Runs as part of `npm run lint`; unit-tested in scripts/check-api-types.test.ts.
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

export const DOMAIN_NOUNS = [
	'Integration',
	'Account',
	'Vendor',
	'Tenant',
	'Transformer',
	'Capability',
	'Breaker',
	'DeadLetter',
	'ChangeLog',
	'Snapshot',
	'ApiKey',
	'RegistrarEndpoint',
	'AdminError'
];
const ALLOWED_SUFFIXES = /(Props|Options)$/;
const ALLOW_COMMENT = /api-types-guard:\s*allow\b/;
const DECL =
	/\b(?:export\s+)?(?:declare\s+)?(interface|type|enum|class)\s+([A-Za-z_$][\w$]*)\s*(<[^=>{]*>)?\s*(=\s*)?(\{)?/g;
const RAW_FETCH = /\bfetch\s*\(\s*[`'"][^`'"]*\/admin\/v1/;

/** Reads `components.schemas` keys from the generated schema file. */
export function schemaNamesFrom(schemaSource) {
	const start = schemaSource.indexOf('export interface components {');
	const end = schemaSource.indexOf('    responses: {', start);
	if (start < 0 || end < 0) throw new Error('schema.d.ts: components.schemas not found');
	return new Set(
		[...schemaSource.slice(start, end).matchAll(/^ {8}([A-Za-z0-9_]+)\??: /gm)].map((m) => m[1])
	);
}

/** Returns violations for one file's source. */
export function findViolations(source, schemaNames, file = '<input>') {
	const out = [];
	const lines = source.split('\n');
	lines.forEach((line, i) => {
		if (ALLOW_COMMENT.test(line)) return;
		const at = `${file}:${i + 1}`;
		for (const m of line.matchAll(DECL)) {
			const [, kind, name, , eq, brace] = m;
			if (schemaNames.has(name)) {
				out.push(
					`${at}: ${kind} ${name} redeclares contract schema '${name}' (alias it in src/lib/api/types.ts)`
				);
				continue;
			}
			const objectShaped = kind === 'interface' || (kind === 'type' && eq && brace);
			if (
				objectShaped &&
				!ALLOWED_SUFFIXES.test(name) &&
				DOMAIN_NOUNS.some((n) => name.includes(n))
			) {
				out.push(
					`${at}: ${kind} ${name} looks like a hand-written API type (use generated types from $lib/api)`
				);
			}
		}
		if (RAW_FETCH.test(line)) {
			out.push(`${at}: raw fetch to /admin/v1 (use adminClient() from $lib/api/client)`);
		}
	});
	return out;
}

function* walk(dir) {
	for (const entry of readdirSync(dir)) {
		const p = join(dir, entry);
		if (statSync(p).isDirectory()) yield* walk(p);
		else if (/\.(ts|js|svelte)$/.test(entry) && !entry.endsWith('.d.ts')) yield p;
	}
}

function main() {
	const root = join(fileURLToPath(new URL('.', import.meta.url)), '..');
	const apiDir = join(root, 'src', 'lib', 'api') + sep;
	const names = schemaNamesFrom(readFileSync(join(apiDir, 'schema.d.ts'), 'utf8'));
	const violations = [];
	for (const file of walk(join(root, 'src'))) {
		if (file.startsWith(apiDir)) continue;
		violations.push(...findViolations(readFileSync(file, 'utf8'), names, relative(root, file)));
	}
	if (violations.length) {
		console.error(
			`check-api-types: ${violations.length} violation(s)\n  ${violations.join('\n  ')}`
		);
		process.exit(1);
	}
	console.log(
		`check-api-types: ok (${names.size} contract schema names, no hand-written API types)`
	);
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) main();

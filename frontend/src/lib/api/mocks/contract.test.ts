// @vitest-environment node
import { Ajv2020 } from 'ajv/dist/2020.js';
import addFormats from 'ajv-formats';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import YAML from 'yaml';
import { buildMocks, render } from '../../../../scripts/extract-examples.mjs';
import { REQUEST_ID_HEADER } from '../client';
import { contractMocks } from './contract.gen';
import { operationIds } from './handlers';

// HUB-252: the mocks are only useful if they ARE the contract. These tests validate every fixture
// (success example + error envelopes) against the hub-admin.v1.yaml schemas with JSON Schema
// 2020-12 (Ajv), and exercise every MSW handler end to end.

const SPEC_PATH = resolve(process.cwd(), '../contracts/openapi/hub-admin.v1.yaml');
const specText = readFileSync(SPEC_PATH, 'utf8');
const spec = YAML.parse(specText);
const SPEC_ID = 'https://hub.internal/contracts/hub-admin.v1.yaml';

const ajv = new Ajv2020({ strict: false, allErrors: true });
addFormats(ajv);
ajv.addSchema({ ...spec, $id: SPEC_ID });

const pointer = (...tokens: string[]) =>
	'#/' +
	tokens.map((t) => encodeURIComponent(t.replace(/~/g, '~0').replace(/\//g, '~1'))).join('/');

/** Validator for the JSON body of `responses[status]` of an operation (follows response $refs). */
function responseValidator(path: string, method: string, status: string) {
	const ref = spec.paths[path][method].responses[status];
	const target = ref.$ref
		? `${SPEC_ID}${ref.$ref}/content/application~1json/schema`
		: `${SPEC_ID}${pointer('paths', path, method, 'responses', status, 'content', 'application/json', 'schema')}`;
	return ajv.compile({ $ref: target });
}

function errorsOf(validate: { errors?: unknown }) {
	return JSON.stringify(validate.errors, null, 1);
}

const contractOps = Object.entries(
	spec.paths as Record<string, Record<string, { operationId?: string }>>
)
	.flatMap(([, item]) => Object.values(item).map((op) => op?.operationId))
	.filter((id): id is string => typeof id === 'string');

describe('contract mocks', () => {
	it('cover every admin operation in the contract', () => {
		expect(contractOps).toHaveLength(79);
		expect([...operationIds].sort()).toEqual([...contractOps].sort());
	});

	it('contract.gen.ts is up to date with the contract (run `npm run gen:api`)', () => {
		const expected = render(buildMocks(spec), spec.info.version);
		const actual = readFileSync(
			resolve(process.cwd(), 'src/lib/api/mocks/contract.gen.ts'),
			'utf8'
		);
		expect(actual === expected).toBe(true);
	});

	describe.each(operationIds.map((op) => [op, contractMocks[op]] as const))('%s', (op, m) => {
		it(`success example (${m.status}) validates against the contract schema`, () => {
			const validate = responseValidator(m.path, m.method, String(m.status));
			expect(validate(m.example), `${op}: ${errorsOf(validate)}`).toBe(true);
		});

		const errorStatuses = Object.keys(m.errors);
		it.each(errorStatuses.length ? errorStatuses : ['(none)'])(
			'error example %s validates against the contract schema',
			(status) => {
				if (status === '(none)') return;
				const validate = responseValidator(m.path, m.method, status);
				expect(validate(m.errors[status]), `${op} ${status}: ${errorsOf(validate)}`).toBe(true);
			}
		);

		it('MSW handler answers with the contract example, status and request id', async () => {
			const url =
				'http://hub.test/admin/v1' + m.path.replace(/\{[^}]+\}/g, 'sample_1') + '?reason=mock';
			const hasBody = m.method !== 'get' && m.method !== 'delete';
			const res = await fetch(url, {
				method: m.method.toUpperCase(),
				headers: hasBody ? { 'Content-Type': 'application/json' } : undefined,
				body: hasBody ? '{}' : undefined
			});
			expect(res.status).toBe(m.status);
			expect(res.headers.get(REQUEST_ID_HEADER)).toMatch(/^req_[0-9A-HJKMNP-TV-Z]{26}$/);
			for (const [h, v] of Object.entries(m.headers)) expect(res.headers.get(h)).toBe(v);
			const body = await res.json();
			expect(body).toEqual(m.example);
			const validate = responseValidator(m.path, m.method, String(m.status));
			expect(validate(body), errorsOf(validate)).toBe(true);
		});
	});

	it('validators reject fixtures that break the contract (sanity check)', () => {
		const validate = responseValidator('/me', 'get', '200');
		const me = contractMocks.getMe.example;
		expect(validate(me)).toBe(true);
		expect(validate({ ...me, roles: ['superuser'] })).toBe(false);
		expect(validate({ ...me, token_expires_at: 'yesterday' })).toBe(false);
		const { subject: _subject, ...withoutSubject } = me;
		expect(validate(withoutSubject)).toBe(false);
	});

	it('every contract error example is the canonical envelope', () => {
		const validate = ajv.compile({ $ref: `${SPEC_ID}#/components/schemas/AdminErrorEnvelope` });
		for (const op of operationIds) {
			for (const [status, envelope] of Object.entries(contractMocks[op].errors)) {
				expect(validate(envelope), `${op} ${status}: ${errorsOf(validate)}`).toBe(true);
			}
		}
	});
});

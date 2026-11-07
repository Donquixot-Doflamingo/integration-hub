import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import {
	fieldRows,
	fromJsonSchema,
	inferFromSample,
	isEmptySchema,
	mergeInferred,
	schemaDocumentProblem,
	setType,
	skeletonOf,
	toJsonSchema,
	treeProblems
} from './json-schema';

// HUB-436 (ADR-0023): the schema registry model behind the SchemaEditor.

const draftBody =
	contractMocks.createIntegrationDraft.example.integration_version.spec.operations.default!
		.request_schema.body;

describe('JSON Schema <-> tree', () => {
	it.each([
		['the contract draft body (nested, x-pii, format)', draftBody],
		[
			'the agify query schema',
			{ type: 'object', required: ['name'], properties: { name: { type: 'string', minLength: 1 } } }
		],
		['a nullable union', { type: 'object', properties: { age: { type: ['integer', 'null'] } } }],
		[
			'an array of objects',
			{
				type: 'object',
				required: ['d'],
				properties: {
					d: {
						type: 'array',
						minItems: 1,
						items: {
							type: 'object',
							required: ['identity'],
							properties: { identity: { type: 'string' }, type: { const: 'event' } }
						}
					}
				}
			}
		],
		['keywords the tree has no column for', { type: 'object', additionalProperties: false }],
		['an unsupported union', { type: ['string', 'integer'] }],
		['an empty document', {}]
	])('round-trips %s', (_n, schema) => {
		expect(toJsonSchema(fromJsonSchema(schema))).toEqual(schema);
	});

	it('reads required fields from `required` and keeps their order', () => {
		const root = fromJsonSchema(draftBody);
		expect(root.properties.map((p) => [p.name, p.required])).toEqual([
			['user', true],
			['event', true],
			['context', false]
		]);
	});

	it('toggling a field writes `required`, and drops it when none is left', () => {
		const root = fromJsonSchema({
			type: 'object',
			required: ['a'],
			properties: { a: { type: 'string' }, b: { type: 'integer' } }
		});
		root.properties[1]!.required = true;
		expect(toJsonSchema(root).required).toEqual(['a', 'b']);
		root.properties[0]!.required = false;
		root.properties[1]!.required = false;
		expect('required' in toJsonSchema(root)).toBe(false);
	});

	it('switching a field to object or array starts it empty', () => {
		const root = inferFromSample({ a: 'x' });
		setType(root.properties[0]!.node, 'array');
		expect(toJsonSchema(root).properties).toEqual({
			a: { type: 'array', items: { type: 'string' } }
		});
		setType(root.properties[0]!.node, 'object');
		expect(toJsonSchema(root).properties).toEqual({ a: { type: 'object' } });
	});
});

describe('inference from a sample', () => {
	it('builds every field with its type, nested objects and arrays, all required', () => {
		const schema = toJsonSchema(
			inferFromSample({
				firstName: 'Mich',
				age: 57,
				score: 0.5,
				vip: false,
				note: null,
				user: { id: 'u_1' },
				tags: ['a', 'b']
			})
		);
		expect(schema).toEqual({
			type: 'object',
			required: ['firstName', 'age', 'score', 'vip', 'note', 'user', 'tags'],
			properties: {
				firstName: { type: 'string' },
				age: { type: 'integer' },
				score: { type: 'number' },
				vip: { type: 'boolean' },
				note: { type: 'null' },
				user: { type: 'object', required: ['id'], properties: { id: { type: 'string' } } },
				tags: { type: 'array', items: { type: 'string' } }
			}
		});
	});

	it('merges array items: fields some items lack are optional, mixed numbers are numbers', () => {
		const schema = toJsonSchema(
			inferFromSample({ d: [{ id: 'a', ts: 1 }, { id: 'b' }], n: [1, 2.5], mixed: [1, 'x'], e: [] })
		);
		expect(schema.properties).toEqual({
			d: {
				type: 'array',
				items: {
					type: 'object',
					required: ['id'],
					properties: { id: { type: 'string' }, ts: { type: 'integer' } }
				}
			},
			n: { type: 'array', items: { type: 'number' } },
			mixed: { type: 'array', items: {} },
			e: { type: 'array' }
		});
	});

	it('a new sample keeps required flags, descriptions and constraints of fields still there', () => {
		const old = fromJsonSchema({
			type: 'object',
			required: ['firstName'],
			properties: {
				firstName: { type: 'string', minLength: 1, description: 'Given name', 'x-pii': 'name' },
				lastName: { type: 'string' }
			}
		});
		const merged = mergeInferred(
			inferFromSample({ firstName: 'A', lastName: 'B', city: 'C' }),
			old
		);
		expect(toJsonSchema(merged)).toEqual({
			type: 'object',
			required: ['firstName', 'city'],
			properties: {
				firstName: { type: 'string', description: 'Given name', minLength: 1, 'x-pii': 'name' },
				lastName: { type: 'string' },
				city: { type: 'string' }
			}
		});
	});
});

describe('checks', () => {
	it('finds empty and duplicate names, nested too', () => {
		const root = inferFromSample({ a: 1, user: { id: 'x' } });
		root.properties[0]!.name = ' ';
		root.properties[1]!.node.properties.push({ ...root.properties[1]!.node.properties[0]! });
		expect(treeProblems(root)).toEqual([
			{ path: '(unnamed)', message: 'Give the field a name.' },
			{ path: 'user.id', message: '"id" is declared twice.' }
		]);
	});

	it.each([
		[{ type: 'obj' }, /"type" must be one of/],
		[{ type: 'object', required: 'a' }, /"required" must be a list/],
		[{ properties: [] }, /"properties" must be an object/],
		[{ properties: { a: { type: 'strin' } } }, /^a: "type"/],
		[[], /must be a JSON object/]
	])('refuses %j', (doc, msg) => {
		expect(schemaDocumentProblem(doc)).toMatch(msg);
	});

	it('accepts the contract examples', () => {
		expect(schemaDocumentProblem(draftBody)).toBeNull();
	});
});

describe('reading', () => {
	it('lists fields depth first with type, required, PII and constraints', () => {
		const rows = fieldRows(draftBody);
		expect(rows.map((r) => [r.path, r.type, r.required])).toEqual([
			['user', 'object', true],
			['user.id', 'string', true],
			['event', 'object', true],
			['event.name', 'string', true],
			['event.timestamp', 'string', false],
			['event.properties', 'object', false],
			['context', 'object', false],
			['context.campaign', 'string', false]
		]);
		expect(rows[1]!.pii).toBe('identifier');
		expect(rows[4]!.constraints).toEqual(['format date-time']);
	});

	it('names array items and nullable types', () => {
		const rows = fieldRows({
			type: 'object',
			properties: {
				d: { type: 'array', items: { type: 'object', properties: { id: { type: 'string' } } } },
				age: { type: ['integer', 'null'] }
			}
		});
		expect(rows.map((r) => [r.path, r.type])).toEqual([
			['d', 'array of object'],
			['d[].id', 'string'],
			['age', 'integer or null']
		]);
	});

	it.each([
		[{}, true],
		[{ type: 'object' }, true],
		[{ type: 'object', properties: {} }, true],
		[{ type: 'object', properties: { a: {} } }, false],
		[{ type: 'object', additionalProperties: false }, false]
	])('isEmptySchema(%j) = %s', (s, want) => expect(isEmptySchema(s)).toBe(want));
});

describe('sample skeletons', () => {
	it('fills required fields with placeholders; const and enum as they are', () => {
		expect(
			skeletonOf({
				type: 'object',
				required: ['name', 'n', 'kind', 'tags', 'opts'],
				properties: {
					name: { type: 'string', minLength: 1 },
					n: { type: 'integer' },
					kind: { const: 'event' },
					tags: { type: 'array' },
					opts: { type: 'object', required: ['on'], properties: { on: { type: 'boolean' } } },
					optional: { type: 'string' }
				}
			})
		).toEqual({ name: '', n: 0, kind: 'event', tags: [], opts: { on: false } });
	});
});

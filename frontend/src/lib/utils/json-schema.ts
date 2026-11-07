/**
 * The schema registry model (HUB-436, ADR-0023). Operators build JSON Schema 2020-12 documents
 * field by field: an integration operation's input (body, headers) and a vendor endpoint's
 * request (path, query, headers, body). A field listed in `required` is required, every other
 * declared field is optional.
 *
 * The console edits a small tree (`SchemaNode`) and converts it to and from JSON Schema. Keywords
 * the tree has no column for (`minLength`, `pattern`, `format`, `enum`, `const`, `x-pii`,
 * `additionalProperties`, ...) are kept in `extra` and written back unchanged, so a schema
 * written by hand survives a round trip through the field view. Pure functions only: the
 * components and their tests share them.
 */

export const FIELD_TYPES = [
	'string',
	'number',
	'integer',
	'boolean',
	'object',
	'array',
	'null',
	'any'
] as const;
/** JSON Schema `type`, or `any` when the schema has no `type` keyword. */
export type FieldType = (typeof FIELD_TYPES)[number];

export const typeLabels: Record<FieldType, string> = {
	string: 'string',
	number: 'number',
	integer: 'integer',
	boolean: 'boolean',
	object: 'object',
	array: 'array',
	null: 'null',
	any: 'any'
};

/** One schema (a field's value, an array's items, or the root). */
export interface SchemaNode {
	/** Stable key for keyed lists; never written to the schema. */
	id: number;
	type: FieldType;
	/** `type: [x, "null"]`: the value may also be null. */
	nullable: boolean;
	description: string;
	/** Keywords kept as they are, in their original order. */
	extra: Record<string, unknown>;
	/** `type: object`: the declared fields, in order. */
	properties: SchemaProperty[];
	/** `type: array`: the schema of each item (null = items of any kind). */
	items: SchemaNode | null;
}

/** One named field of an object. */
export interface SchemaProperty {
	name: string;
	required: boolean;
	node: SchemaNode;
}

/** The keywords the tree models itself; everything else goes to `extra`. */
const MODELLED = new Set(['type', 'description', 'properties', 'required', 'items']);

let nextId = 1;
export function newNode(type: FieldType = 'string'): SchemaNode {
	return {
		id: nextId++,
		type,
		nullable: false,
		description: '',
		extra: {},
		properties: [],
		items: null
	};
}

const isObject = (v: unknown): v is Record<string, unknown> =>
	typeof v === 'object' && v !== null && !Array.isArray(v);

/** The JSON type of a sample value (integers are `integer`). */
export function typeOfValue(v: unknown): FieldType {
	if (v === null) return 'null';
	if (Array.isArray(v)) return 'array';
	switch (typeof v) {
		case 'string':
			return 'string';
		case 'boolean':
			return 'boolean';
		case 'number':
			return Number.isInteger(v) ? 'integer' : 'number';
		case 'object':
			return 'object';
		default:
			return 'any';
	}
}

// ---- JSON Schema -> tree ------------------------------------------------------------------------

function readType(raw: unknown): { type: FieldType; nullable: boolean; keep: boolean } {
	if (raw === undefined) return { type: 'any', nullable: false, keep: false };
	const list = Array.isArray(raw) ? raw : [raw];
	const known = list.filter((t): t is FieldType =>
		(FIELD_TYPES as readonly string[]).includes(String(t))
	);
	if (known.length !== list.length) return { type: 'any', nullable: false, keep: true };
	const nonNull = known.filter((t) => t !== 'null');
	if (list.length === 1) return { type: known[0]!, nullable: false, keep: false };
	if (list.length === 2 && nonNull.length === 1)
		return { type: nonNull[0]!, nullable: true, keep: false };
	// Unions the tree cannot show (["string", "integer"]): kept as they are.
	return { type: 'any', nullable: false, keep: true };
}

/** A tree for a JSON Schema document (any JSON object is a document). */
export function fromJsonSchema(schema: unknown): SchemaNode {
	const node = newNode('any');
	if (!isObject(schema)) return node;
	const t = readType(schema.type);
	node.type = t.type;
	node.nullable = t.nullable;
	if (t.keep) node.extra.type = schema.type;
	if (typeof schema.description === 'string') node.description = schema.description;
	else if (schema.description !== undefined) node.extra.description = schema.description;
	for (const [k, v] of Object.entries(schema)) if (!MODELLED.has(k)) node.extra[k] = v;
	const required = Array.isArray(schema.required)
		? schema.required.filter((r): r is string => typeof r === 'string')
		: [];
	if (isObject(schema.properties)) {
		// A schema with properties but no type (`{properties: ...}`) is shown as an object.
		if (node.type === 'any' && !t.keep && schema.type === undefined) node.type = 'object';
		for (const [name, sub] of Object.entries(schema.properties)) {
			node.properties.push({ name, required: required.includes(name), node: fromJsonSchema(sub) });
		}
	} else if (schema.properties !== undefined) {
		node.extra.properties = schema.properties;
	}
	// `required` names without a property (allowed by JSON Schema) are kept as they are.
	const orphans = required.filter((r) => !node.properties.some((p) => p.name === r));
	if (orphans.length) node.extra.required = orphans;
	if (Array.isArray(schema.required) && required.length !== schema.required.length)
		node.extra.required = schema.required;
	if (isObject(schema.items)) node.items = fromJsonSchema(schema.items);
	else if (schema.items !== undefined) node.extra.items = schema.items;
	return node;
}

// ---- tree -> JSON Schema ------------------------------------------------------------------------

/** The JSON Schema document of a tree. */
export function toJsonSchema(node: SchemaNode): Record<string, unknown> {
	const out: Record<string, unknown> = {};
	if (node.type !== 'any') out.type = node.nullable ? [node.type, 'null'] : node.type;
	if (node.description.trim()) out.description = node.description.trim();
	const extraRequired = Array.isArray(node.extra.required)
		? (node.extra.required as unknown[])
		: [];
	for (const [k, v] of Object.entries(node.extra)) if (k !== 'required') out[k] = v;
	if (node.properties.length) {
		const required = node.properties.filter((p) => p.required).map((p) => p.name);
		const all = [...required, ...extraRequired.filter((r) => !required.includes(r as string))];
		if (all.length) out.required = all;
		out.properties = Object.fromEntries(node.properties.map((p) => [p.name, toJsonSchema(p.node)]));
	} else if (extraRequired.length) {
		out.required = extraRequired;
	}
	if (node.type === 'array' && node.items) out.items = toJsonSchema(node.items);
	return out;
}

// ---- sample -> tree -----------------------------------------------------------------------------

/**
 * A tree from a sample JSON value: every field found becomes a declared field with the type of
 * its value, nested objects and arrays included. Fields of the sample start as required (the
 * operator unticks the optional ones). Array items merge every element: a field that some
 * elements lack is optional, mixed item types become `any`.
 */
export function inferFromSample(value: unknown): SchemaNode {
	const node = newNode(typeOfValue(value));
	if (node.type === 'object') {
		for (const [name, v] of Object.entries(value as Record<string, unknown>)) {
			node.properties.push({ name, required: true, node: inferFromSample(v) });
		}
	} else if (node.type === 'array') {
		node.items = inferItems(value as unknown[]);
	}
	return node;
}

function inferItems(values: unknown[]): SchemaNode | null {
	if (!values.length) return null;
	const types = new Set(values.map(typeOfValue));
	if (types.size === 2 && types.has('integer') && types.has('number')) return newNode('number');
	if (types.size > 1) return newNode('any');
	const only = [...types][0]!;
	if (only === 'object') {
		const objects = values as Record<string, unknown>[];
		const node = newNode('object');
		const names: string[] = [];
		for (const o of objects) for (const k of Object.keys(o)) if (!names.includes(k)) names.push(k);
		for (const name of names) {
			const present = objects.filter((o) => name in o);
			const sub =
				present.length === 1
					? inferFromSample(present[0]![name])
					: inferItems(present.map((o) => o[name]));
			node.properties.push({
				name,
				required: present.length === objects.length,
				node: sub ?? newNode('array')
			});
		}
		return node;
	}
	if (only === 'array') {
		const node = newNode('array');
		node.items = inferItems((values as unknown[][]).flat());
		return node;
	}
	return newNode(only);
}

/**
 * Keeps what the operator already said about fields that are still there after a new sample:
 * required/optional, description and the extra keywords, when the type did not change.
 */
export function mergeInferred(fresh: SchemaNode, old: SchemaNode | null): SchemaNode {
	if (!old || old.type !== fresh.type) return fresh;
	fresh.description = old.description;
	fresh.nullable = old.nullable;
	fresh.extra = { ...old.extra };
	for (const p of fresh.properties) {
		const before = old.properties.find((o) => o.name === p.name);
		if (!before) continue;
		p.required = before.required;
		p.node = mergeInferred(p.node, before.node);
	}
	if (fresh.items) fresh.items = mergeInferred(fresh.items, old.items);
	return fresh;
}

// ---- edits --------------------------------------------------------------------------------------

/** Sets a node's type; switching to object or array starts it empty. */
export function setType(node: SchemaNode, type: FieldType): void {
	node.type = type;
	if (type !== 'object') node.properties = [];
	if (type !== 'array') node.items = null;
	else if (!node.items) node.items = newNode('string');
	if (type === 'null' || type === 'any') node.nullable = false;
}

/** A field name not used yet among `props` (`field`, `field_2`, ...). */
export function freshName(props: readonly SchemaProperty[], base = 'field'): string {
	if (!props.some((p) => p.name === base)) return base;
	for (let i = 2; ; i++) if (!props.some((p) => p.name === `${base}_${i}`)) return `${base}_${i}`;
}

/** Where fields of `node` live: the node itself (object) or its array items (object items). */
export function fieldsHolder(node: SchemaNode): SchemaNode | null {
	if (node.type === 'object') return node;
	if (node.type === 'array' && node.items?.type === 'object') return node.items;
	return null;
}

// ---- checks -------------------------------------------------------------------------------------

export interface SchemaProblem {
	/** Dotted field path (`user.id`, `tags[]`), or '' for the document itself. */
	path: string;
	message: string;
}

/**
 * Problems the console can see in the field list before sending (empty and duplicate names).
 * The Hub compiles the schema and answers with a pointer for anything else.
 */
export function treeProblems(node: SchemaNode, path = ''): SchemaProblem[] {
	const out: SchemaProblem[] = [];
	const seen = new Set<string>();
	for (const p of node.properties) {
		const name = p.name.trim();
		const at = path ? `${path}.${name || '(unnamed)'}` : name || '(unnamed)';
		if (!name) out.push({ path: at, message: 'Give the field a name.' });
		else if (seen.has(name)) out.push({ path: at, message: `"${name}" is declared twice.` });
		seen.add(name);
		out.push(...treeProblems(p.node, at));
	}
	if (node.items) out.push(...treeProblems(node.items, `${path}[]`));
	return out;
}

const VALID_TYPES: readonly string[] = FIELD_TYPES.filter((t) => t !== 'any');

/**
 * Structural checks of a JSON Schema document typed in the JSON view: what the field view needs
 * to show it, plus the mistakes a hand-written schema makes most (`type` misspelt, `required`
 * not a list, `properties` not an object). Null when it looks fine.
 */
export function schemaDocumentProblem(doc: unknown, at = ''): string | null {
	const where = at || 'the schema';
	if (!isObject(doc)) return `${where} must be a JSON object ({ ... }).`;
	if (doc.type !== undefined) {
		const list = Array.isArray(doc.type) ? doc.type : [doc.type];
		const bad = list.find((t) => !VALID_TYPES.includes(String(t)));
		if (bad !== undefined || !list.length)
			return `${where}: "type" must be one of ${VALID_TYPES.join(', ')} (got ${JSON.stringify(bad ?? doc.type)}).`;
	}
	if (doc.required !== undefined) {
		if (!Array.isArray(doc.required) || doc.required.some((r) => typeof r !== 'string'))
			return `${where}: "required" must be a list of field names.`;
	}
	if (doc.properties !== undefined) {
		if (!isObject(doc.properties)) return `${where}: "properties" must be an object.`;
		for (const [name, sub] of Object.entries(doc.properties)) {
			if (!name.trim()) return `${where}: a field has an empty name.`;
			const p = schemaDocumentProblem(sub, at ? `${at}.${name}` : name);
			if (p) return p;
		}
	}
	if (doc.items !== undefined && typeof doc.items !== 'boolean') {
		const p = schemaDocumentProblem(doc.items, `${at || 'items'}[]`);
		if (p) return p;
	}
	return null;
}

// ---- reading ------------------------------------------------------------------------------------

/** One row of a read-only field list. */
export interface FieldRow {
	/** Dotted path: `user.id`, `tags[]`, `items[].name`. */
	path: string;
	depth: number;
	type: string;
	required: boolean;
	description: string;
	/** `x-pii` tag, if any (drives audit masking). */
	pii: string | null;
	/** Other constraints, readable: `minLength 1`, `pattern ^[0-9]{12}$`. */
	constraints: string[];
}

const CONSTRAINTS = [
	'minLength',
	'maxLength',
	'pattern',
	'format',
	'minimum',
	'maximum',
	'minItems',
	'maxItems',
	'const',
	'enum'
] as const;

function typeText(n: SchemaNode): string {
	if (n.type === 'array') return n.items ? `array of ${typeText(n.items)}` : 'array';
	const raw = n.extra.type;
	if (n.type === 'any' && Array.isArray(raw)) return raw.join(' or ');
	return n.nullable ? `${n.type} or null` : n.type;
}

/** The declared fields of a schema as rows, depth first, for read-only views. */
export function fieldRows(schema: unknown): FieldRow[] {
	const out: FieldRow[] = [];
	const walk = (node: SchemaNode, prefix: string, depth: number) => {
		for (const p of node.properties) {
			const path = prefix ? `${prefix}.${p.name}` : p.name;
			out.push(row(p.node, path, depth, p.required));
			walkInner(p.node, path, depth + 1);
		}
	};
	const walkInner = (node: SchemaNode, path: string, depth: number) => {
		if (node.type === 'object') walk(node, path, depth);
		else if (node.type === 'array' && node.items?.type === 'object')
			walk(node.items, `${path}[]`, depth);
	};
	const row = (n: SchemaNode, path: string, depth: number, required: boolean): FieldRow => ({
		path,
		depth,
		type: typeText(n),
		required,
		description: n.description,
		pii: typeof n.extra['x-pii'] === 'string' ? (n.extra['x-pii'] as string) : null,
		constraints: CONSTRAINTS.filter((k) => n.extra[k] !== undefined).map(
			(k) => `${k} ${typeof n.extra[k] === 'string' ? n.extra[k] : JSON.stringify(n.extra[k])}`
		)
	});
	walkInner(fromJsonSchema(schema), '', 0);
	return out;
}

/** True when a schema declares nothing (no fields, no constraints): `{}` or `{type: object}`. */
export function isEmptySchema(schema: unknown): boolean {
	if (!isObject(schema)) return true;
	const keys = Object.keys(schema).filter((k) => k !== 'type' || schema.type !== 'object');
	return keys.every(
		(k) =>
			(k === 'properties' &&
				isObject(schema.properties) &&
				!Object.keys(schema.properties).length) ||
			(k === 'required' && Array.isArray(schema.required) && !schema.required.length)
	);
}

// ---- samples ------------------------------------------------------------------------------------

/**
 * An example value that fits a schema, to start a sample from: required fields only (optional
 * ones are listed in the schema but left out), placeholders by type (`""`, `0`, `false`, `[]`),
 * `const` and the first `enum` value as they are.
 */
export function skeletonOf(schema: unknown): unknown {
	if (!isObject(schema)) return {};
	if (schema.const !== undefined) return schema.const;
	if (Array.isArray(schema.enum) && schema.enum.length) return schema.enum[0];
	if (schema.default !== undefined) return schema.default;
	const t = readType(schema.type);
	const type = t.type === 'any' && isObject(schema.properties) ? 'object' : t.type;
	switch (type) {
		case 'object': {
			const required = Array.isArray(schema.required) ? schema.required : [];
			const props = isObject(schema.properties) ? schema.properties : {};
			return Object.fromEntries(
				Object.entries(props)
					.filter(([k]) => required.includes(k))
					.map(([k, v]) => [k, skeletonOf(v)])
			);
		}
		case 'array':
			return [];
		case 'string':
			return '';
		case 'integer':
		case 'number':
			return 0;
		case 'boolean':
			return false;
		case 'null':
			return null;
		default:
			return null;
	}
}

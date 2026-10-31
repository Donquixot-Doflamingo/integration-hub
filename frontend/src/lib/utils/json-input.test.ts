import { describe, expect, it } from 'vitest';
import { lineColumn, parseJsonObject, prettyJson } from './json-input';

// HUB-269: client-side JSON checks for the transformer dry-run.

describe('parseJsonObject', () => {
	it('accepts an object', () => {
		expect(parseJsonObject('{ "user": { "id": "u_1" } }')).toEqual({
			ok: true,
			value: { user: { id: 'u_1' } }
		});
	});

	it.each([
		['{ "a": 1, }', 1],
		['{\n  "a": 1\n  "b": 2\n}', 3],
		['{\n  "a": ', 2]
	])('reports the line of a syntax error: %j', (text, line) => {
		const r = parseJsonObject(text, { what: 'Input' });
		expect(r.ok).toBe(false);
		if (r.ok) return;
		expect(r.message).toMatch(/^Input is not valid JSON/);
		expect(r.line).toBe(line);
	});

	it.each([
		['[1, 2]', 'must be a JSON object ({ ... }), not an array'],
		['null', 'not null'],
		['"text"', 'not a string'],
		['42', 'not a number']
	])('refuses non-objects: %s', (text, msg) => {
		const r = parseJsonObject(text);
		expect(r.ok).toBe(false);
		if (!r.ok) expect(r.message).toContain(msg);
	});

	it('empty is an error unless optional', () => {
		expect(parseJsonObject('  ', { what: 'Input' })).toMatchObject({
			ok: false,
			message: 'Input is empty. Enter a JSON object.'
		});
		expect(parseJsonObject('', { optional: true })).toEqual({ ok: true, value: undefined });
	});

	it('header values must be strings', () => {
		expect(
			parseJsonObject('{"X-Trace": 1}', { stringValues: true, what: 'Headers' })
		).toMatchObject({
			ok: false,
			message: 'Headers: "X-Trace" must be a string.'
		});
		expect(parseJsonObject('{"X-Trace": "a"}', { stringValues: true }).ok).toBe(true);
	});
});

describe('helpers', () => {
	it('lineColumn', () => {
		expect(lineColumn('ab\ncd', 4)).toEqual({ line: 2, column: 2 });
		expect(lineColumn('', 0)).toEqual({ line: 1, column: 1 });
	});
	it('prettyJson', () => {
		expect(prettyJson({ a: 1 })).toBe('{\n  "a": 1\n}');
		expect(prettyJson(undefined)).toBe('');
	});
});

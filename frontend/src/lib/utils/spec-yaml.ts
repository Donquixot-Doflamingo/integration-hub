import { LineCounter, parseDocument, stringify, isMap, isSeq } from 'yaml';

/**
 * The spec editor (HUB-259) edits an integration spec as YAML, the format operators already read
 * in hubctl configs and in the version diff. JSON is valid YAML, so pasted JSON works too.
 * Validation errors from the admin API carry JSON pointers into the spec; `pointerLines` maps them
 * to lines of the text the operator is looking at.
 */

/** Spec object -> editor text (block style, no line folding). */
export function specToYaml(spec: unknown): string {
	return stringify(spec, { lineWidth: 0 });
}

export type ParsedSpec =
	| { ok: true; value: Record<string, unknown> }
	| { ok: false; message: string; line: number | null };

/** Editor text -> spec object, or the first syntax problem with its line. */
export function parseSpecYaml(text: string): ParsedSpec {
	if (!text.trim()) return { ok: false, message: 'The spec is empty.', line: null };
	const lineCounter = new LineCounter();
	const doc = parseDocument(text, { lineCounter, prettyErrors: true, uniqueKeys: true });
	const err = doc.errors[0];
	if (err) {
		const line = err.linePos?.[0]?.line ?? null;
		return { ok: false, message: err.message.split('\n')[0] ?? 'Invalid YAML', line };
	}
	const value: unknown = doc.toJS();
	if (typeof value !== 'object' || value === null || Array.isArray(value)) {
		return {
			ok: false,
			message: 'The spec must be a mapping (targets, delivery, audit, ...).',
			line: 1
		};
	}
	return { ok: true, value: value as Record<string, unknown> };
}

/** `/targets/1/account_id` -> `['targets', 1, 'account_id']` (RFC 6901 unescaping). */
export function pointerTokens(pointer: string): (string | number)[] {
	if (!pointer || pointer === '/') return [];
	return pointer
		.split('/')
		.slice(1)
		.map((t) => t.replace(/~1/g, '/').replace(/~0/g, '~'))
		.map((t) => (/^\d+$/.test(t) ? Number(t) : t));
}

/**
 * The 1-based line of `pointer` in `text`, or of its nearest ancestor that exists (a missing
 * property is reported at its parent). Null when the text does not parse or nothing matches.
 */
export function pointerLine(text: string, pointer: string): number | null {
	const lineCounter = new LineCounter();
	const doc = parseDocument(text, { lineCounter });
	if (doc.errors.length) return null;
	const tokens = pointerTokens(pointer);
	for (let n = tokens.length; n >= 1; n--) {
		const path = tokens.slice(0, n);
		const parent = n > 1 ? doc.getIn(path.slice(0, -1), true) : doc.contents;
		const last = path[path.length - 1]!;
		if (isMap(parent)) {
			// Point at the key, where the operator reads the field name.
			const pair = parent.items.find((p) => (p.key as { value?: unknown } | null)?.value === last);
			const range = (pair?.key as { range?: [number, number, number] } | null)?.range;
			if (range) return lineCounter.linePos(range[0]).line;
		} else if (isSeq(parent) && typeof last === 'number') {
			const item = parent.items[last] as { range?: [number, number, number] } | undefined;
			if (item?.range) return lineCounter.linePos(item.range[0]).line;
		}
	}
	return null;
}

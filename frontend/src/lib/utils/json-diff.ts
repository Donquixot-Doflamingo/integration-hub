import type { DiffOp } from '$lib/api/types';

/**
 * Structural diff of two JSON values as `DiffOp`s (the contract's add / remove / replace with a
 * JSON pointer), for the change log's before / after (HUB-274), which the API returns as two
 * objects without a diff. Objects are compared key by key (keys of `before` first, then new keys
 * of `after`), arrays index by index, everything else by value. `null` / `undefined` on one side
 * of the root (a create or a delete) lists each top-level field of the other side.
 */
export function jsonDiff(before: unknown, after: unknown): DiffOp[] {
	const out: DiffOp[] = [];
	const root = (v: unknown) => (v == null ? {} : v);
	walk(root(before), root(after), '', out);
	return out;
}

function isObject(v: unknown): v is Record<string, unknown> {
	return typeof v === 'object' && v !== null && !Array.isArray(v);
}

const escape = (key: string) => key.replace(/~/g, '~0').replace(/\//g, '~1');

function walk(a: unknown, b: unknown, path: string, out: DiffOp[]) {
	if (isObject(a) && isObject(b)) {
		const keys = [...Object.keys(a), ...Object.keys(b).filter((k) => !(k in a))];
		for (const k of keys) {
			const p = `${path}/${escape(k)}`;
			if (!(k in b)) out.push({ op: 'remove', path: p, before: a[k] });
			else if (!(k in a)) out.push({ op: 'add', path: p, after: b[k] });
			else walk(a[k], b[k], p, out);
		}
		return;
	}
	if (Array.isArray(a) && Array.isArray(b)) {
		const n = Math.max(a.length, b.length);
		for (let i = 0; i < n; i++) {
			const p = `${path}/${i}`;
			if (i >= b.length) out.push({ op: 'remove', path: p, before: a[i] });
			else if (i >= a.length) out.push({ op: 'add', path: p, after: b[i] });
			else walk(a[i], b[i], p, out);
		}
		return;
	}
	if (!sameValue(a, b)) out.push({ op: 'replace', path: path || '', before: a, after: b });
}

function sameValue(a: unknown, b: unknown): boolean {
	if (a === b) return true;
	return JSON.stringify(a) === JSON.stringify(b);
}

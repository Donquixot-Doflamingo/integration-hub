import type { DiffOp } from '$lib/api/types';

/**
 * Helpers for the version diff view (HUB-258). The admin diff endpoint returns both a structured
 * list of changes (RFC 6902-style ops on the spec) and a unified diff of the YAML renderings; the
 * side-by-side view is built from the unified text, the path list from the ops.
 */

export type Side = { text: string; type: 'context' | 'removed' | 'added' };
export type SplitRow =
	| { kind: 'hunk'; text: string }
	| { kind: 'line'; left: Side | null; right: Side | null }
	| { kind: 'fold'; id: number; rows: SplitRow[] };

export interface SplitDiff {
	/** Titles from the `---` / `+++` header lines, e.g. "v7" and "v8". */
	oldTitle: string | null;
	newTitle: string | null;
	rows: SplitRow[];
}

/**
 * Unified diff -> side-by-side rows. Runs of removed lines followed by added lines are paired
 * line by line (old left, new right); the longer run gets blanks on the other side.
 */
export function splitUnified(unified: string): SplitDiff {
	let oldTitle: string | null = null;
	let newTitle: string | null = null;
	const rows: SplitRow[] = [];
	if (unified.trim() === '') return { oldTitle, newTitle, rows };
	let removed: Side[] = [];
	let added: Side[] = [];

	const flush = () => {
		const n = Math.max(removed.length, added.length);
		for (let i = 0; i < n; i++) {
			rows.push({ kind: 'line', left: removed[i] ?? null, right: added[i] ?? null });
		}
		removed = [];
		added = [];
	};

	const lines = unified.replace(/\n$/, '').split('\n');
	for (const line of lines) {
		if (line.startsWith('--- ') && oldTitle === null && rows.length === 0) {
			oldTitle = line.slice(4).trim();
		} else if (line.startsWith('+++ ') && newTitle === null && rows.length === 0) {
			newTitle = line.slice(4).trim();
		} else if (line.startsWith('@@')) {
			flush();
			rows.push({ kind: 'hunk', text: line.replace(/^@@\s*|\s*@@$/g, '') });
		} else if (line.startsWith('-')) {
			if (added.length) flush();
			removed.push({ text: line.slice(1), type: 'removed' });
		} else if (line.startsWith('+')) {
			added.push({ text: line.slice(1), type: 'added' });
		} else if (line.startsWith('\\')) {
			// "\ No newline at end of file": not content.
		} else {
			flush();
			const text = line.startsWith(' ') ? line.slice(1) : line;
			rows.push({
				kind: 'line',
				left: { text, type: 'context' },
				right: { text, type: 'context' }
			});
		}
	}
	flush();
	return { oldTitle, newTitle, rows };
}

/** Collapses runs of more than `keep * 2 + min` unchanged lines, keeping `keep` on each side. */
export function foldUnchanged(rows: SplitRow[], keep = 3, min = 4): SplitRow[] {
	const out: SplitRow[] = [];
	let run: SplitRow[] = [];
	let id = 0;
	const isContext = (r: SplitRow) => r.kind === 'line' && r.left?.type === 'context';
	const flush = () => {
		if (run.length > keep * 2 + min) {
			out.push(...run.slice(0, keep));
			out.push({ kind: 'fold', id: id++, rows: run.slice(keep, run.length - keep) });
			out.push(...run.slice(run.length - keep));
		} else {
			out.push(...run);
		}
		run = [];
	};
	for (const r of rows) {
		if (isContext(r)) run.push(r);
		else {
			flush();
			out.push(r);
		}
	}
	flush();
	return out;
}

/** JSON pointer -> readable path: `/targets/0/weight` -> `targets[0].weight`. */
export function pointerToPath(pointer: string): string {
	if (pointer === '' || pointer === '/') return '(whole spec)';
	return pointer
		.split('/')
		.slice(1)
		.map((s) => s.replace(/~1/g, '/').replace(/~0/g, '~'))
		.reduce((acc, seg) => (/^\d+$/.test(seg) ? `${acc}[${seg}]` : acc ? `${acc}.${seg}` : seg), '');
}

export const opLabels: Record<DiffOp['op'], { label: string; symbol: string }> = {
	add: { label: 'Added', symbol: '+' },
	remove: { label: 'Removed', symbol: '-' },
	replace: { label: 'Changed', symbol: '~' }
};

/** "2 paths changed (1 added, 1 changed): targets[0].weight, targets[1]" */
export function summarize(changes: DiffOp[]): string {
	if (changes.length === 0) return 'No differences.';
	const count = (op: DiffOp['op']) => changes.filter((c) => c.op === op).length;
	const parts = [
		[count('add'), 'added'],
		[count('remove'), 'removed'],
		[count('replace'), 'changed']
	]
		.filter(([n]) => (n as number) > 0)
		.map(([n, w]) => `${n} ${w}`);
	const noun = changes.length === 1 ? 'path' : 'paths';
	const paths = changes.map((c) => pointerToPath(c.path)).join(', ');
	return `${changes.length} ${noun} differ (${parts.join(', ')}): ${paths}`;
}

/** Compact, readable JSON for a before/after value. `undefined` means "not present". */
export function formatValue(v: unknown): string {
	if (v === undefined) return '';
	if (typeof v === 'string') return v;
	return JSON.stringify(v, null, 2);
}

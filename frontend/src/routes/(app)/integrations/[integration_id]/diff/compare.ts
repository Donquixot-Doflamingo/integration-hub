import type { IntegrationVersionSummary } from '$lib/api/types';

export type DiffView = 'split' | 'paths';

/** `from` / `to` / `view` from the URL; versions must be positive integers. */
export function parseCompare(params: URLSearchParams): {
	from: number | null;
	to: number | null;
	view: DiffView;
} {
	const num = (k: string) => {
		const v = Number(params.get(k));
		return Number.isInteger(v) && v >= 1 ? v : null;
	};
	return {
		from: num('from'),
		to: num('to'),
		view: params.get('view') === 'paths' ? 'paths' : 'split'
	};
}

/**
 * Default pair when the URL names none: the newest version against the active one, or against
 * the version it was based on, or the one before it. Null when there is nothing to compare.
 */
export function defaultPair(
	versions: IntegrationVersionSummary[]
): { from: number; to: number } | null {
	if (versions.length < 2) return null;
	const sorted = [...versions].sort((a, b) => b.version - a.version);
	const to = sorted[0]!;
	const active = sorted.find((v) => v.is_active);
	if (active && active.version !== to.version) return { from: active.version, to: to.version };
	if (to.based_on != null && versions.some((v) => v.version === to.based_on)) {
		return { from: to.based_on, to: to.version };
	}
	return { from: sorted[1]!.version, to: to.version };
}

/** `?from=7&to=8` (+ `view=paths` when not the default). */
export function compareSearch(from: number, to: number, view: DiffView = 'split'): string {
	const p = new URLSearchParams({ from: String(from), to: String(to) });
	if (view === 'paths') p.set('view', 'paths');
	return `?${p}`;
}

/** Option label in the version pickers: "v8 (draft)", "v7 (active)". */
export function versionOption(v: IntegrationVersionSummary): string {
	const tag = v.is_active ? ' (active)' : v.state === 'draft' ? ' (draft)' : '';
	return `v${v.version}${tag}`;
}

/**
 * Cursor paging in the URL (admin-console-ux §5). The API only returns `next_cursor`, so the way
 * back is kept in the URL too: `prev` holds the cursors of the earlier pages in order (an empty
 * value is the first page). Links stay shareable and survive reload.
 */
export const PAGE_SIZES = [25, 50, 100, 200] as const;
export const DEFAULT_PAGE_SIZE = 50;
const MAX_CURSOR = 512;
/** Bound on the back trail so URLs stay reasonable. */
const MAX_TRAIL = 20;
/**
 * First entry of a trail that had to drop its oldest cursors (HUB-455). Past it the page number is
 * unknown and Previous stops at the oldest page the trail still knows: without the marker the
 * number restarted at 21 and Previous jumped to the first page. `~` cannot start a cursor.
 */
const TRUNCATED = '~';

export interface Paging {
	cursor: string | undefined;
	prev: string[];
	limit: number;
}

export function parsePaging(params: URLSearchParams): Paging {
	const cursor = params.get('cursor') || undefined;
	const limitRaw = Number(params.get('limit'));
	const limit = (PAGE_SIZES as readonly number[]).includes(limitRaw) ? limitRaw : DEFAULT_PAGE_SIZE;
	return {
		cursor: cursor && cursor.length <= MAX_CURSOR ? cursor : undefined,
		prev: params
			.getAll('prev')
			.filter((c) => c.length <= MAX_CURSOR)
			.slice(-MAX_TRAIL),
		limit
	};
}

/** Query string pieces for a page: `cursor`, `prev` (repeated) and `limit` when not default. */
export function pagingParams(p: Paging): [string, string][] {
	const out: [string, string][] = [];
	if (p.cursor) out.push(['cursor', p.cursor]);
	for (const c of p.prev) out.push(['prev', c]);
	if (p.limit !== DEFAULT_PAGE_SIZE) out.push(['limit', String(p.limit)]);
	return out;
}

export function nextPaging(p: Paging, nextCursor: string): Paging {
	let prev = [...p.prev, p.cursor ?? ''];
	if (prev.length > MAX_TRAIL) {
		const known = prev.filter((c) => c !== TRUNCATED);
		prev = [TRUNCATED, ...known.slice(-(MAX_TRAIL - 1))];
	}
	return { ...p, cursor: nextCursor, prev };
}

/** The previous page, or null on the first page. */
export function previousPaging(p: Paging): Paging | null {
	if (!p.cursor) return null;
	const prev = [...p.prev];
	const back = prev.pop();
	// The trail ends here: the page before this one is not known, so Previous is not offered.
	if (back === TRUNCATED) return null;
	return { ...p, cursor: back || undefined, prev };
}

/** 1-based page number, or null once the trail was truncated (the API has no totals to recover it). */
export function pageNumber(p: Paging): number | null {
	if (p.prev[0] === TRUNCATED) return null;
	return p.cursor ? p.prev.length + 1 : 1;
}

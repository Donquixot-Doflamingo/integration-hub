import type { ApiResult } from './client';

/** Any contract list body: `{ items, next_cursor }` (cursor pagination, contracts/README.md). */
export interface ListPage<T> {
	items: T[];
	next_cursor: string | null;
}

/** Item type of a list operation's success body. */
export type ListItem<R> = R extends ApiResult<{ items: (infer I)[] }> ? I : never;

/**
 * Follows `next_cursor` until the list ends (or `maxPages`), returning one page with every item.
 * For short lists a screen must show whole (an integration's versions). Stops at the first error.
 * If the cap is hit, `next_cursor` is the cursor of the first page not fetched.
 */
export async function collectPages<T>(
	page: (cursor: string | undefined) => Promise<ApiResult<ListPage<T>>>,
	maxPages = 25
): Promise<ApiResult<ListPage<T>>> {
	const items: T[] = [];
	let cursor: string | undefined;
	let last: ApiResult<ListPage<T>> | undefined;
	for (let n = 0; n < maxPages; n++) {
		last = await page(cursor);
		if (!last.ok) return last;
		items.push(...last.data.items);
		cursor = last.data.next_cursor ?? undefined;
		if (!cursor) break;
	}
	if (!last || !last.ok) throw new Error('collectPages: maxPages must be at least 1');
	return { ...last, data: { items, next_cursor: cursor ?? null } };
}

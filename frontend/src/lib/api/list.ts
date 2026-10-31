import type { ApiResult } from './client';

/** Any contract list body: `{ items, next_cursor }` (cursor pagination, contracts/README.md). */
export interface ListPage<T> {
	items: T[];
	next_cursor: string | null;
}

/** Item type of a list operation's success body. */
export type ListItem<R> = R extends ApiResult<{ items: (infer I)[] }> ? I : never;

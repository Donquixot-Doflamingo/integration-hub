import { describe, expect, it } from 'vitest';
import { nextPaging, pageNumber, pagingParams, parsePaging, previousPaging } from './paging';

const q = (s: string) => new URLSearchParams(s);

describe('parsePaging', () => {
	it.each([
		{ s: '', want: { cursor: undefined, prev: [], limit: 50 } },
		{ s: 'cursor=c2&prev=&prev=c1&limit=25', want: { cursor: 'c2', prev: ['', 'c1'], limit: 25 } },
		{ s: 'limit=7', want: { cursor: undefined, prev: [], limit: 50 } },
		{ s: 'limit=500', want: { cursor: undefined, prev: [], limit: 50 } },
		{ s: `cursor=${'x'.repeat(513)}`, want: { cursor: undefined, prev: [], limit: 50 } }
	])('$s', ({ s, want }) => {
		expect(parsePaging(q(s))).toEqual(want);
	});
});

describe('walking pages', () => {
	it('next then previous returns to the first page', () => {
		const p1 = parsePaging(q(''));
		const p2 = nextPaging(p1, 'c1');
		const p3 = nextPaging(p2, 'c2');
		expect(p3).toEqual({ cursor: 'c2', prev: ['', 'c1'], limit: 50 });
		expect(pageNumber(p3)).toBe(3);
		expect(previousPaging(p3)).toEqual(p2);
		expect(previousPaging(p2)).toEqual({ cursor: undefined, prev: [], limit: 50 });
		expect(previousPaging(p1)).toBeNull();
		expect(pageNumber(p1)).toBe(1);
	});

	it('serialises only what differs from the defaults', () => {
		expect(pagingParams(parsePaging(q('')))).toEqual([]);
		expect(pagingParams({ cursor: 'c2', prev: ['', 'c1'], limit: 100 })).toEqual([
			['cursor', 'c2'],
			['prev', ''],
			['prev', 'c1'],
			['limit', '100']
		]);
	});
});

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

	it('keeps the page number exact up to page 21, then stops claiming one (HUB-455)', () => {
		let p = parsePaging(q(''));
		for (let n = 2; n <= 21; n++) p = nextPaging(p, `c${n}`);
		expect(pageNumber(p)).toBe(21);
		p = nextPaging(p, 'c22');
		expect(pageNumber(p)).toBeNull();
		for (let n = 23; n <= 30; n++) p = nextPaging(p, `c${n}`);
		expect(pageNumber(p)).toBeNull();
		expect(p.prev.length).toBeLessThanOrEqual(20);
	});

	it('Previous steps back one page at a time, and is not offered where the trail ends (HUB-455)', () => {
		let p = parsePaging(q(''));
		for (let n = 2; n <= 30; n++) p = nextPaging(p, `c${n}`);
		const seen: (string | undefined)[] = [];
		for (let back = previousPaging(p); back; back = previousPaging(back)) seen.push(back.cursor);
		// c29, c28, ... down to the oldest cursor the trail still knows; never a jump to page 1.
		expect(seen[0]).toBe('c29');
		expect(seen.every((c) => c !== undefined)).toBe(true);
		expect(seen.length).toBeGreaterThan(15);
	});

	it('survives the URL round trip once truncated', () => {
		let p = parsePaging(q(''));
		for (let n = 2; n <= 30; n++) p = nextPaging(p, `c${n}`);
		const url = new URLSearchParams(pagingParams(p));
		expect(parsePaging(url)).toEqual(p);
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

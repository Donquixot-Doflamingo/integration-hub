import { describe, expect, it, vi } from 'vitest';
import type { ApiResult } from './client';
import { HubApiError } from './errors';
import { collectPages, type ListPage } from './list';

const page = (items: number[], next: string | null): ApiResult<ListPage<number>> => ({
	ok: true,
	status: 200,
	requestId: null,
	etag: null,
	data: { items, next_cursor: next }
});

describe('collectPages', () => {
	it('follows next_cursor to the end', async () => {
		const fetchPage = vi.fn(async (c: string | undefined) =>
			c === undefined ? page([3, 2], 'c1') : c === 'c1' ? page([1], 'c2') : page([], null)
		);
		const r = await collectPages(fetchPage);
		expect(r).toMatchObject({ ok: true, data: { items: [3, 2, 1], next_cursor: null } });
		expect(fetchPage.mock.calls.map((c) => c[0])).toEqual([undefined, 'c1', 'c2']);
	});

	it('stops at the cap and reports where it stopped', async () => {
		const r = await collectPages(async (c) => page([1], `${c ?? ''}x`), 3);
		expect(r).toMatchObject({ ok: true, data: { items: [1, 1, 1], next_cursor: 'xxx' } });
	});

	it('returns the first error', async () => {
		const error = new HubApiError({ status: 404, code: 'NOT_FOUND', message: 'gone' });
		const r = await collectPages(async (c) =>
			c ? { ok: false, error, status: 404, requestId: null } : page([1], 'c1')
		);
		expect(r).toMatchObject({ ok: false, error: { code: 'NOT_FOUND' } });
	});
});

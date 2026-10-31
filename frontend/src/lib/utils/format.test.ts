import { describe, expect, it } from 'vitest';
import { formatBytes, formatDurationMs, formatNumber, formatRatio, formatUtc } from './format';

describe('format', () => {
	it.each([
		['2026-10-31T09:40:30Z', '2026-10-31 09:40 UTC'],
		['2026-10-31T15:10:00+05:30', '2026-10-31 09:40 UTC'],
		[null, '-'],
		['not a date', 'not a date']
	])('formatUtc(%s) = %s', (iso, want) => expect(formatUtc(iso)).toBe(want));

	it.each([
		[1290, '1,290'],
		[0, '0'],
		[undefined, '-']
	])('formatNumber(%s) = %s', (n, want) => expect(formatNumber(n)).toBe(want));

	it.each([
		[1840, '1,840 ms'],
		[61_000, '61 s'],
		[5_400_000, '90 min'],
		[14_400_000, '4 h'],
		[null, '-']
	])('formatDurationMs(%s) = %s', (ms, want) => expect(formatDurationMs(ms)).toBe(want));

	it.each([
		[512, '512 B'],
		[48_213, '47.1 KiB'],
		[5 * 1024 * 1024, '5.0 MiB']
	])('formatBytes(%s) = %s', (n, want) => expect(formatBytes(n)).toBe(want));

	it.each([
		[0.82, '82%'],
		[undefined, '-']
	])('formatRatio(%s) = %s', (r, want) => expect(formatRatio(r)).toBe(want));
});

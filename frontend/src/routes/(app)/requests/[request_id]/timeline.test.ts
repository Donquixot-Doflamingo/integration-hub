import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { clock, gap, payloadView, timed } from './timeline';

// HUB-264: events in seq order with their gaps; payload view per audit policy.

const events = contractMocks.getRequestTimeline.example.events;

describe('timed', () => {
	it('sorts by time (seq breaks ties) and measures gaps from the previous event and from the start', () => {
		const t = timed([...events].reverse());
		expect(t.map((x) => x.event.seq)).toEqual([1, 2, 3, 4, 5]);
		expect(t.map((x) => x.sincePrevious)).toEqual([null, 1, 0, 212, 1786]);
		expect(t.map((x) => x.sinceStart)).toEqual([0, 1, 1, 213, 1999]);
	});

	it('orders a replay after the original events, with no negative gaps (HUB-455)', () => {
		// A replay restarts seq at 1, but it happens later: time is the order, seq only breaks ties.
		const [first, second] = events;
		const replay = { ...first!, at: '2026-10-31T10:00:00.000Z' };
		const t = timed([replay, second!, first!]);
		expect(t.map((x) => [x.event.seq, x.event.at])).toEqual([
			[first!.seq, first!.at],
			[second!.seq, second!.at],
			[replay.seq, replay.at]
		]);
		expect(t.every((x) => x.sincePrevious === null || x.sincePrevious >= 0)).toBe(true);
	});

	it('uses seq to order events with the same timestamp', () => {
		const at = '2026-10-31T09:15:00.000Z';
		const t = timed([
			{ ...events[2]!, seq: 3, at },
			{ ...events[1]!, seq: 2, at }
		]);
		expect(t.map((x) => x.event.seq)).toEqual([2, 3]);
	});

	it('handles no events', () => expect(timed([])).toEqual([]));
});

describe('formatting', () => {
	it.each([
		[null, 'start'],
		[0, '+0 ms'],
		[1786, '+1,786 ms'],
		[12_345, '+12.3 s']
	])('gap(%j) = %s', (ms, want) => expect(gap(ms)).toBe(want));

	it('clock', () => expect(clock('2026-10-31T09:15:00.214Z')).toBe('09:15:00.214 UTC'));
});

describe('payloadView', () => {
	const e = events[0]!;
	it.each([
		[{ ...e, sealed: true, payload: null }, 'sealed', 'sealed'],
		[{ ...e, payload: null }, 'metadata_only', 'metadata_only'],
		[{ ...e, payload: null }, 'masked', 'none'],
		[e, 'masked', 'shown']
	])('%#', (event, policy, kind) => expect(payloadView(event, policy).kind).toBe(kind));
});

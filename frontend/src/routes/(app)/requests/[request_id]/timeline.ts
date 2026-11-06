import type { TimelineEvent, TimelineEventType } from '$lib/api/types';
import type { PayloadView } from '$lib/utils/payload';

/** DESIGN §13.1 event types in the operator's words. */
export const eventLabels: Record<TimelineEventType, string> = {
	'request.received': 'Request received',
	'request.validated': 'Validated against the capability schema',
	'integration.resolved': 'Integration resolved',
	'idempotency.checked': 'Idempotency key checked',
	'request.transformed': 'Transformed to the vendor request',
	'policy.applied': 'Delivery policy applied',
	'request.persisted': 'Persisted',
	'request.accepted': 'Accepted (asynchronous)',
	'request.parked': 'Parked',
	'vendor.sent': 'Sent to the vendor',
	'vendor.responded': 'Vendor responded',
	'response.transformed': 'Response transformed',
	'response.transform_failed': 'Response transform failed',
	'callback.delivered': 'Callback delivered',
	'request.completed': 'Completed',
	'request.failed': 'Failed',
	'request.expired': 'Expired'
};

export interface TimedEvent {
	event: TimelineEvent;
	/** ms since the previous event (null for the first). */
	sincePrevious: number | null;
	/** ms since the first event. */
	sinceStart: number;
}

/**
 * Events in `seq` order with their gaps (the contract promises seq order; sorted anyway). A real
 * Hub repeats `seq` values after a DLQ replay (the replay's events reuse 101, 102, ...), so equal
 * seqs are ordered by time and the page keys rows by position, not by seq (HUB-289).
 */
export function timed(events: TimelineEvent[]): TimedEvent[] {
	const sorted = [...events].sort((a, b) => a.seq - b.seq || Date.parse(a.at) - Date.parse(b.at));
	const start = sorted.length ? Date.parse(sorted[0]!.at) : 0;
	let prev: number | null = null;
	return sorted.map((event) => {
		const at = Date.parse(event.at);
		const out = { event, sincePrevious: prev === null ? null : at - prev, sinceStart: at - start };
		prev = at;
		return out;
	});
}

/** `2026-10-31T09:15:00.214Z` -> `09:15:00.214 UTC`. */
export function clock(iso: string): string {
	const d = new Date(iso);
	return Number.isNaN(d.getTime()) ? iso : `${d.toISOString().slice(11, 23)} UTC`;
}

/** Gap label: `+212 ms`, `+1.6 s`. */
export function gap(ms: number | null): string {
	if (ms === null) return 'start';
	if (ms < 0) return `${ms} ms`;
	if (ms < 10_000) return `+${ms.toLocaleString('en-US')} ms`;
	return `+${(ms / 1000).toFixed(1)} s`;
}

export type { PayloadView };

/** How to show an event's payload under the audit policy. */
export function payloadView(e: TimelineEvent, policy: string): PayloadView {
	if (e.sealed) return { kind: 'sealed' };
	if (e.payload) return { kind: 'shown', value: e.payload };
	if (policy === 'metadata_only') return { kind: 'metadata_only' };
	return { kind: 'none' };
}

import type { IconName } from '$lib/components/icons';
import type { BreakerState, RequestState } from '$lib/api/types';

export interface StatusView {
	tone: 'ok' | 'warn' | 'danger' | 'neutral';
	icon: IconName;
	label: string;
}

/** Breaker state -> text + icon + tone (admin-console-ux §4). */
export const breakerStatus: Record<BreakerState, StatusView> = {
	closed: { tone: 'ok', icon: 'closed', label: 'Closed' },
	half_open: { tone: 'warn', icon: 'halfOpen', label: 'Half-open' },
	open: { tone: 'danger', icon: 'open', label: 'Open' }
};

/** Worst first: open, half-open, closed. */
export const breakerSeverity: Record<BreakerState, number> = { open: 0, half_open: 1, closed: 2 };

export const requestStatus: Record<RequestState, StatusView> = {
	accepted: { tone: 'neutral', icon: 'requests', label: 'Accepted' },
	in_flight: { tone: 'neutral', icon: 'requests', label: 'In flight' },
	succeeded: { tone: 'ok', icon: 'closed', label: 'Succeeded' },
	retry_scheduled: { tone: 'warn', icon: 'retry', label: 'Retry scheduled' },
	parked: { tone: 'warn', icon: 'pause', label: 'Parked' },
	dead_lettered: { tone: 'danger', icon: 'dlq', label: 'Dead-lettered' },
	expired: { tone: 'neutral', icon: 'changelog', label: 'Expired' },
	failed: { tone: 'danger', icon: 'open', label: 'Failed' }
};

/** Integration state in text + icon: paused wins, then "no active version", else live. */
export function integrationStatus(i: {
	paused: boolean;
	active_version: number | null;
}): StatusView {
	if (i.paused) return { tone: 'neutral', icon: 'pause', label: 'Paused' };
	if (i.active_version == null) return { tone: 'warn', icon: 'alert', label: 'No active version' };
	return { tone: 'ok', icon: 'closed', label: 'Live' };
}

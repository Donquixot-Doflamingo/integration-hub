export type ForceKind = 'open' | 'close' | 'clear';

/** Hold durations offered for a forced breaker (contract: 1 s .. 24 h, null = until cleared). */
export const DURATIONS: { label: string; ms: number | null }[] = [
	{ label: '15 minutes', ms: 900_000 },
	{ label: '30 minutes', ms: 1_800_000 },
	{ label: '1 hour', ms: 3_600_000 },
	{ label: '4 hours', ms: 14_400_000 },
	{ label: '24 hours', ms: 86_400_000 },
	{ label: 'Until someone clears it', ms: null }
];

export const DEFAULT_DURATION_MS = 1_800_000;

/** Form value -> `duration_ms` (empty = null = until cleared). */
export function parseDuration(
	raw: string
): { ok: true; value: number | null } | { ok: false; message: string } {
	const v = raw.trim();
	if (v === '') return { ok: true, value: null };
	if (!/^\d+$/.test(v)) return { ok: false, message: 'Pick how long to hold the breaker.' };
	const ms = Number(v);
	if (ms < 1000 || ms > 86_400_000)
		return { ok: false, message: 'Hold for between 1 second and 24 hours, or until cleared.' };
	return { ok: true, value: ms };
}

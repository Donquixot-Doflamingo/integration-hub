import type { DlqBulkResult } from '$lib/api/types';
import { formatNumber } from '$lib/utils/format';
import { isFiltered, type Filters } from './filters';

/**
 * DLQ replay and purge by filter (HUB-267). Both are two steps: a dry run counts what the current
 * filters match (the exact number from the API, shown before anything is submitted), then the
 * operator confirms with a reason; purge also needs that count typed. Both act on every page of
 * matches, not only the page on screen.
 */
export type BulkKind = 'replay' | 'purge';

/**
 * The contract requires a `reason` even on a dry run (`DlqReplayRequest` / `DlqPurgeRequest`). A
 * dry run changes nothing and returns no `change_id`, so the console sends this fixed text; the
 * operator's own reason goes with the real call.
 */
export const DRY_RUN_REASON = 'Console dry run: count the dead letters matching these filters';

/** At most this many entries per call (contract); `truncated: true` means call again. */
export const PER_CALL_LIMIT = 10_000;

export function plural(n: number, one: string, many: string): string {
	return `${formatNumber(n)} ${n === 1 ? one : many}`;
}

/** "1 dead letter" / "1,284 dead letters". */
export const deadLetters = (n: number) => plural(n, 'dead letter', 'dead letters');

/** The count a dry run promises for the real call (capped per call). */
export function willAffect(r: Pick<DlqBulkResult, 'matched'>): number {
	return Math.min(r.matched, PER_CALL_LIMIT);
}

/** Dialog copy for a counted selection. */
export function bulkCopy(kind: BulkKind, count: Pick<DlqBulkResult, 'matched'>, filters: Filters) {
	const n = willAffect(count);
	const scope = isFiltered(filters) ? 'matching these filters' : 'in the DLQ (no filter set)';
	return kind === 'replay'
		? {
				title: `Replay ${deadLetters(n)}`,
				verb: 'Replay',
				busy: 'Replaying...',
				action: '?/replay',
				consequence: `Moves the ${deadLetters(n)} ${scope} back to accepted. They re-resolve and re-transform, so a transformer fix applies, and are sent to the vendor again.`
			}
		: {
				title: `Purge ${deadLetters(n)}`,
				verb: 'Purge',
				busy: 'Purging...',
				action: '?/purge',
				consequence: `Permanently removes the ${deadLetters(n)} ${scope} from the DLQ. They cannot be replayed afterwards. The request ledger and audit stay (retention rules apply).`
			};
}

/** Warnings to show in the dialog, in plain words. */
export function bulkWarnings(
	kind: BulkKind,
	count: Pick<DlqBulkResult, 'matched'>,
	filters: Filters
): string[] {
	const out: string[] = [];
	if (count.matched > PER_CALL_LIMIT) {
		out.push(
			`${deadLetters(count.matched)} match, more than the ${formatNumber(PER_CALL_LIMIT)} one call handles: this ${kind} covers the first ${formatNumber(PER_CALL_LIMIT)}. Count again afterwards for the rest.`
		);
	}
	if (kind === 'replay' && filters.replayed !== 'false') {
		out.push(
			filters.replayed === 'true'
				? 'These entries were already replayed once; replaying sends them to the vendor again.'
				: 'These filters include entries that were already replayed; replaying sends them to the vendor again. To skip them, cancel and set Replayed to "Not replayed yet".'
		);
	}
	return out;
}

/** Success notice text. */
export function bulkDone(kind: BulkKind, r: DlqBulkResult): string {
	const verb = kind === 'replay' ? 'Replayed' : 'Purged';
	const more = r.truncated ? ' More entries match: count again to continue.' : '';
	const tail =
		kind === 'replay'
			? ' They are back in the queue and will be delivered again.'
			: ' The request ledger and audit stay.';
	const matched = r.matched !== r.affected ? ` (${formatNumber(r.matched)} matched)` : '';
	return `${verb} ${deadLetters(r.affected)}${matched}.${tail}${more}`;
}

import type { AccountState, AccountSummary, Breaker } from '$lib/api/types';
import { breakerSeverity } from '$lib/status';
import type { StatusView } from '$lib/status';

/**
 * Health board model (HUB-261): `listBreakers` rows joined with the account lifecycle state from
 * `listAccounts`, grouped vendor -> account -> endpoint and sorted worst first
 * (admin-console-ux §4): open, half-open, closed; then most parked, oldest parked.
 * Dead letters are per account (`dead_letter_count` / `dead_letter_oldest_at` on `listAccounts`,
 * hub-admin 1.1.0, CCR-003 / HUB-284), so every endpoint row of an account shows the same figure.
 */
export interface DlqFigure {
	/** Not replayed, within the 14-day retention; may lag by up to a minute (contract). */
	count: number;
	/** When the oldest of them was dead-lettered; null when there are none. */
	oldestAt: string | null;
}

export interface BoardRow {
	breaker: Breaker;
	/** Lifecycle state of the account; null when `listAccounts` did not return it. */
	lifecycle: AccountState | null;
	/** The account's dead letters; null when unknown (account missing, or no figure sent). */
	deadLetters: DlqFigure | null;
}

export interface BoardGroup {
	vendorId: string;
	rows: BoardRow[];
}

export interface BoardSummary {
	open: number;
	halfOpen: number;
	closed: number;
	forced: number;
	parked: number;
}

/** The account's dead-letter figure, or null when the API did not send one. */
export function deadLettersOf(a: AccountSummary | undefined): DlqFigure | null {
	if (!a || typeof a.dead_letter_count !== 'number') return null;
	return { count: a.dead_letter_count, oldestAt: a.dead_letter_oldest_at ?? null };
}

/** Dead letters across the accounts on the board (each account once); null if none is known. */
export function totalDeadLetters(breakers: Breaker[], accounts: AccountSummary[]): number | null {
	const byId = new Map(accounts.map((a) => [a.account_id, a]));
	let total: number | null = null;
	for (const id of new Set(breakers.map((b) => b.account_id))) {
		const d = deadLettersOf(byId.get(id));
		if (d) total = (total ?? 0) + d.count;
	}
	return total;
}

function compareRows(a: BoardRow, b: BoardRow): number {
	const x = a.breaker;
	const y = b.breaker;
	return (
		breakerSeverity[x.state] - breakerSeverity[y.state] ||
		(y.parked_count ?? 0) - (x.parked_count ?? 0) ||
		(y.parked_oldest_age_ms ?? 0) - (x.parked_oldest_age_ms ?? 0) ||
		x.account_id.localeCompare(y.account_id) ||
		x.endpoint_ref.localeCompare(y.endpoint_ref)
	);
}

/** Groups by vendor; groups ordered by their worst row, rows worst first. */
export function buildBoard(breakers: Breaker[], accounts: AccountSummary[]): BoardGroup[] {
	const byId = new Map(accounts.map((a) => [a.account_id, a]));
	const rows = breakers
		.map((b) => {
			const a = byId.get(b.account_id);
			return { breaker: b, lifecycle: a?.state ?? null, deadLetters: deadLettersOf(a) };
		})
		.sort(compareRows);
	const groups = new Map<string, BoardRow[]>();
	for (const r of rows) {
		const list = groups.get(r.breaker.vendor_id) ?? [];
		list.push(r);
		groups.set(r.breaker.vendor_id, list);
	}
	// Map keeps insertion order, and rows were sorted worst first, so groups are too.
	return [...groups].map(([vendorId, list]) => ({ vendorId, rows: list }));
}

export function summarizeBoard(breakers: Breaker[]): BoardSummary {
	const s: BoardSummary = { open: 0, halfOpen: 0, closed: 0, forced: 0, parked: 0 };
	for (const b of breakers) {
		if (b.state === 'open') s.open++;
		else if (b.state === 'half_open') s.halfOpen++;
		else s.closed++;
		if (b.forced && b.forced !== 'none') s.forced++;
		s.parked += b.parked_count ?? 0;
	}
	return s;
}

/** "retry in 23 s" for an open breaker, "probe due" once `open_until` has passed. */
export function retryLabel(
	b: Pick<Breaker, 'state' | 'open_until'> & Partial<Pick<Breaker, 'forced'>>,
	now: number
): string | null {
	// A forced-open breaker does not probe; its hold is shown by `forcedLabel`.
	if (b.state !== 'open' || !b.open_until || b.forced === 'open') return null;
	const left = Date.parse(b.open_until) - now;
	if (!Number.isFinite(left)) return null;
	if (left <= 0) return 'probe due';
	return left < 120_000
		? `retry in ${Math.ceil(left / 1000)} s`
		: `retry in ${Math.ceil(left / 60_000)} min`;
}

/** Forced state in words: "Forced open until 10:30 UTC" / "Forced closed until cleared". */
export function forcedLabel(b: Pick<Breaker, 'forced' | 'forced_until'>): string | null {
	if (!b.forced || b.forced === 'none') return null;
	const until = b.forced_until
		? `until ${new Date(b.forced_until).toISOString().slice(11, 16)} UTC`
		: 'until cleared';
	return `Forced ${b.forced} ${until}`;
}

export const lifecycleStatus: Record<AccountState, StatusView> = {
	active: { tone: 'ok', icon: 'closed', label: 'Active' },
	draining: { tone: 'warn', icon: 'retry', label: 'Draining' },
	retired: { tone: 'neutral', icon: 'pause', label: 'Retired' }
};

/** Invalidated by the board's auto-refresh. */
export const HEALTH_DEPENDENCY = 'hub:health';

/** Refresh intervals offered on the board (F-19: at most 5 s by default). */
export const REFRESH_CHOICES = [5, 10, 30, 60] as const;
export const DEFAULT_REFRESH_S = 5;

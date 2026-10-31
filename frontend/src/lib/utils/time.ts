/**
 * Time-range helpers for filter forms (request explorer HUB-263, DLQ HUB-266). Times in the URL
 * and the API are RFC 3339 UTC with second precision; `<input type="datetime-local">` shows UTC.
 */

/** `2026-10-31T09:00` / `...:00Z` / `...:00.000Z` -> `2026-10-31T09:00:00Z`; null if not a time. */
export function normalizeTime(raw: string | null | undefined): string | null {
	if (!raw) return null;
	const v = raw.trim();
	const m = /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2})(:\d{2})?(\.\d+)?(Z)?$/.exec(v);
	if (!m) return null;
	const iso = `${m[1]}T${m[2]}${m[3] ?? ':00'}Z`;
	// Reject impossible dates (2026-02-31 would roll over to March).
	const t = Date.parse(iso);
	return Number.isNaN(t) || `${new Date(t).toISOString().slice(0, 19)}Z` !== iso ? null : iso;
}

/** Value for `<input type="datetime-local">` (shown as UTC): `2026-10-31T09:00`. */
export function toLocalInput(iso: string | undefined): string {
	return iso ? iso.slice(0, 16) : '';
}

/** Quick ranges ending now, as absolute times so the URL stays shareable. */
export function lastRange(ms: number, now = Date.now()): { from: string; to: string } {
	const to = new Date(Math.ceil(now / 60_000) * 60_000);
	const from = new Date(to.getTime() - ms);
	const iso = (d: Date) => `${d.toISOString().slice(0, 19)}Z`;
	return { from: iso(from), to: iso(to) };
}

/** Chip text for a time filter: `2026-10-31 09:00 UTC`. */
export function timeChip(iso: string): string {
	return `${iso.slice(0, 16).replace('T', ' ')} UTC`;
}

/** One timestamp format app-wide: `2026-10-31 09:40 UTC` (admin-console-ux §5). */
export function formatUtc(iso: string | null | undefined): string {
	if (!iso) return '-';
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return iso;
	return `${d.toISOString().slice(0, 16).replace('T', ' ')} UTC`;
}

const numberFormat = new Intl.NumberFormat('en-US');
/** Thousands separators: 1290 -> `1,290`. */
export function formatNumber(n: number | null | undefined): string {
	return typeof n === 'number' && Number.isFinite(n) ? numberFormat.format(n) : '-';
}

/** Durations with units: 61000 -> `61 s`, 1840 -> `1,840 ms`, 5400000 -> `90 min`. */
export function formatDurationMs(ms: number | null | undefined): string {
	if (typeof ms !== 'number' || !Number.isFinite(ms)) return '-';
	if (ms < 10_000) return `${formatNumber(Math.round(ms))} ms`;
	if (ms < 120_000) return `${formatNumber(Math.round(ms / 1000))} s`;
	if (ms < 7_200_000) return `${formatNumber(Math.round(ms / 60_000))} min`;
	return `${formatNumber(Math.round(ms / 3_600_000))} h`;
}

/** Byte sizes: 48213 -> `47.1 KiB`. */
export function formatBytes(n: number | null | undefined): string {
	if (typeof n !== 'number' || !Number.isFinite(n)) return '-';
	if (n < 1024) return `${n} B`;
	const units = ['KiB', 'MiB', 'GiB'];
	let v = n / 1024;
	let i = 0;
	while (v >= 1024 && i < units.length - 1) {
		v /= 1024;
		i++;
	}
	return `${v.toFixed(1)} ${units[i]}`;
}

/** `0.82` -> `82%`. */
export function formatRatio(r: number | null | undefined): string {
	return typeof r === 'number' && Number.isFinite(r) ? `${Math.round(r * 100)}%` : '-';
}

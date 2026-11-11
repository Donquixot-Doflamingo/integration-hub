import type { DlqFilter, ListDeadLettersQuery } from '$lib/api/types';
import { droppedFields, SLUG_PROBLEM, type FilterProblems } from '$lib/utils/filter-problems';
import { pagingParams, parsePaging, type Paging } from '$lib/utils/paging';
import { normalizeTime, timeChip } from '$lib/utils/time';

/**
 * DLQ filters <-> URL query (HUB-266). Names match `listDeadLetters` (and `DlqFilter`, which
 * replay and purge take, HUB-267), so the URL reads like the API call and the same filters drive
 * the list and the bulk actions. Times are RFC 3339 UTC on `dead_lettered_at`. Entries are kept
 * 14 days, so the contract sets no maximum range; a reversed range is refused before calling.
 */
export const TEXT_KEYS = [
	'tenant_id',
	'integration_id',
	'vendor_id',
	'account_id',
	'reason'
] as const;
export const FILTER_KEYS = [...TEXT_KEYS, 'replayed', 'from', 'to'] as const;
export type FilterKey = (typeof FILTER_KEYS)[number];

export const filterLabels: Record<FilterKey, string> = {
	tenant_id: 'Tenant',
	integration_id: 'Integration',
	vendor_id: 'Vendor',
	account_id: 'Account',
	reason: 'Reason',
	replayed: 'Replayed',
	from: 'From',
	to: 'To'
};

export interface Filters {
	tenant_id?: string;
	integration_id?: string;
	vendor_id?: string;
	account_id?: string;
	/** Canonical error code (`UPSTREAM_REJECTED`) or `poison`. */
	reason?: string;
	replayed?: 'true' | 'false';
	from?: string;
	to?: string;
}

/** Contract `Slug`. */
const SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/;
/** A canonical error code or `poison` (contract: `reason` on `listDeadLetters`). */
const REASON = /^([A-Z][A-Z0-9_]{1,63}|poison)$/;

/**
 * Reasons offered as suggestions: the codes that dead-letter at once or after exhausted retries
 * (DESIGN §10.2) and `poison` (§10.6). Any canonical code is accepted.
 */
export const COMMON_REASONS = [
	'UPSTREAM_REJECTED',
	'UPSTREAM_AUTH_FAILED',
	'TRANSFORM_FAILED',
	'UPSTREAM_TIMEOUT',
	'UPSTREAM_UNAVAILABLE',
	'UPSTREAM_RATE_LIMITED',
	'poison'
] as const;

export const replayedChoices = { true: 'Already replayed', false: 'Not replayed yet' } as const;

export interface ListState {
	filters: Filters;
	paging: Paging;
	/** Why the time range cannot be listed (the API is not called), or null. */
	problem: string | null;
	query: ListDeadLettersQuery;
}

export function rangeProblem(from?: string, to?: string): string | null {
	if (!from || !to) return null;
	return Date.parse(to) <= Date.parse(from) ? '"To" must be later than "From".' : null;
}

function valid(key: (typeof TEXT_KEYS)[number], v: string): boolean {
	return key === 'reason' ? REASON.test(v) : SLUG.test(v);
}

export function parseListState(params: URLSearchParams): ListState {
	const filters: Filters = {};
	for (const key of TEXT_KEYS) {
		const v = params.get(key)?.trim();
		if (v && valid(key, v)) filters[key] = v;
	}
	const replayed = params.get('replayed');
	if (replayed === 'true' || replayed === 'false') filters.replayed = replayed;
	const from = normalizeTime(params.get('from'));
	const to = normalizeTime(params.get('to'));
	if (from) filters.from = from;
	if (to) filters.to = to;

	const paging = parsePaging(params);
	const query: ListDeadLettersQuery = { ...toDlqFilter(filters), limit: paging.limit };
	if (paging.cursor) query.cursor = paging.cursor;
	return { filters, paging, problem: rangeProblem(filters.from, filters.to), query };
}

/** The filters as the `DlqFilter` that replay and purge take (HUB-267). Empty = every entry. */
export function toDlqFilter(filters: Filters): DlqFilter {
	const out: DlqFilter = {};
	for (const key of TEXT_KEYS) if (filters[key]) out[key] = filters[key];
	if (filters.replayed) out.replayed = filters.replayed === 'true';
	if (filters.from) out.from = filters.from;
	if (filters.to) out.to = filters.to;
	return out;
}

/** `?...` for these filters and paging (stable order). */
export function searchFor(filters: Filters, paging?: Paging): string {
	const params = new URLSearchParams();
	for (const key of FILTER_KEYS) {
		const v = filters[key];
		if (v) params.set(key, v);
	}
	if (paging) for (const [k, v] of pagingParams(paging)) params.append(k, v);
	const s = params.toString();
	return s ? `?${s}` : '';
}

export function isFiltered(filters: Filters): boolean {
	return FILTER_KEYS.some((k) => !!filters[k]);
}

export function without(filters: Filters, key: FilterKey): Filters {
	const next = { ...filters };
	delete next[key];
	return next;
}

export function chipValue(key: FilterKey, filters: Filters): string {
	if (key === 'replayed') return replayedChoices[filters.replayed ?? 'false'];
	if (key === 'from' || key === 'to') return timeChip(filters[key]!);
	return filters[key] ?? '';
}

const problemMessages: Record<FilterKey, string> = {
	tenant_id: SLUG_PROBLEM,
	integration_id: SLUG_PROBLEM,
	vendor_id: SLUG_PROBLEM,
	account_id: SLUG_PROBLEM,
	reason: 'Use a reason code such as UPSTREAM_REJECTED, or "poison".',
	replayed: 'Pick a listed choice.',
	from: 'Enter a valid date and time.',
	to: 'Enter a valid date and time.'
};

/**
 * The filter form -> filters (empty fields dropped; reason upper-cased unless `poison`).
 * `problems` names each non-empty field that did not fit its rule. The page marks it and does not
 * search or count: the replay/purge count must cover exactly the filters the operator sees
 * (HUB-455).
 */
export function filtersFromForm(form: FormData): {
	filters: Filters;
	limit: number | null;
	problems: FilterProblems<FilterKey>;
} {
	const params = new URLSearchParams();
	const rawText: Partial<Record<FilterKey, string>> = {};
	for (const key of TEXT_KEYS) {
		const raw = form.get(key);
		if (typeof raw !== 'string' || !raw.trim()) continue;
		const v = raw.trim();
		const reason = v.toLowerCase() === 'poison' ? 'poison' : v.toUpperCase();
		params.set(key, key === 'reason' ? reason : v);
		rawText[key] = key === 'reason' ? reason : v;
	}
	const replayed = form.get('replayed');
	if (typeof replayed === 'string' && replayed) {
		params.set('replayed', replayed);
		rawText.replayed = replayed;
	}
	for (const key of ['from', 'to'] as const) {
		const text = String(form.get(key) ?? '').trim();
		if (text) rawText[key] = text;
		const v = normalizeTime(text);
		if (v) params.set(key, v);
	}
	const limit = Number(form.get('limit'));
	const filters = parseListState(params).filters;
	return {
		filters,
		problems: droppedFields(rawText, filters, problemMessages),
		limit: Number.isFinite(limit) && limit > 0 ? limit : null
	};
}

/**
 * Hidden form fields that carry the filters to a bulk action (replay, purge). Prefixed with
 * `filter.`, because the change reason the dialog sends is also called `reason`.
 */
export const FIELD_PREFIX = 'filter.';

export function filterFields(filters: Filters): [string, string][] {
	return FILTER_KEYS.filter((k) => !!filters[k]).map((k) => [FIELD_PREFIX + k, filters[k]!]);
}

/** The filters back from a bulk action's form (validated like the URL). */
export function filtersFromFields(form: FormData): Filters {
	const params = new URLSearchParams();
	for (const key of FILTER_KEYS) {
		const v = form.get(FIELD_PREFIX + key);
		if (typeof v === 'string' && v) params.set(key, v);
	}
	return parseListState(params).filters;
}

import type { ListChangeLogQuery, ObjectType } from '$lib/api/types';
import { pagingParams, parsePaging, type Paging } from '$lib/utils/paging';
import { normalizeTime, timeChip } from '$lib/utils/time';

/**
 * Change log filters <-> URL query (HUB-274). Names match `listChangeLog` (`actor`,
 * `object_type`, `object_id`, `action`, `from`, `to`), so the URL reads like the API call. Times
 * are RFC 3339 UTC on `at`; a reversed range is refused before calling.
 */
export const TEXT_KEYS = ['actor', 'object_id', 'action'] as const;
export const FILTER_KEYS = ['actor', 'object_type', 'object_id', 'action', 'from', 'to'] as const;
export type FilterKey = (typeof FILTER_KEYS)[number];
export type Filters = Partial<Record<FilterKey, string>>;

/** Contract `ObjectType`, in the order the contract lists them. */
export const OBJECT_TYPES: readonly ObjectType[] = [
	'vendor',
	'vendor_endpoint',
	'account',
	'capability',
	'transformer',
	'integration',
	'tenant',
	'api_key',
	'registrar_endpoint',
	'breaker',
	'dlq',
	'request',
	'snapshot'
];

export const objectTypeLabels: Record<ObjectType, string> = {
	vendor: 'Vendor',
	vendor_endpoint: 'Vendor endpoint',
	account: 'Account',
	capability: 'Capability',
	transformer: 'Transformer',
	integration: 'Integration',
	tenant: 'Tenant',
	api_key: 'API key',
	registrar_endpoint: 'Callback endpoint',
	breaker: 'Breaker',
	dlq: 'DLQ',
	request: 'Request',
	snapshot: 'Snapshot'
};

export const filterLabels: Record<FilterKey, string> = {
	actor: 'Actor',
	object_type: 'Object type',
	object_id: 'Object id',
	action: 'Action',
	from: 'From',
	to: 'To'
};

/** Mutating operationIds seen most in the change log (suggestions; any operationId is accepted). */
export const COMMON_ACTIONS = [
	'publishIntegrationVersion',
	'rollbackIntegration',
	'updateIntegrationWeights',
	'pauseIntegration',
	'resumeIntegration',
	'replayDeadLetters',
	'purgeDeadLetters',
	'forceOpenBreaker',
	'forceCloseBreaker',
	'clearBreakerForce',
	'createTenant',
	'updateTenant',
	'createApiKey',
	'rotateApiKey',
	'revokeApiKey',
	'createRegistrarEndpoint',
	'updateRegistrarEndpoint',
	'rotateRegistrarSecret',
	'deleteRegistrarEndpoint'
] as const;

const VALID: Record<(typeof TEXT_KEYS)[number], (v: string) => boolean> = {
	actor: (v) => v.length <= 256,
	object_id: (v) => v.length <= 128,
	action: (v) => /^[a-z][A-Za-z0-9]{1,63}$/.test(v)
};

export interface ListState {
	filters: Filters;
	paging: Paging;
	problem: string | null;
	query: ListChangeLogQuery;
}

export function rangeProblem(from?: string, to?: string): string | null {
	if (!from || !to) return null;
	return Date.parse(to) <= Date.parse(from) ? '"To" must be later than "From".' : null;
}

export function parseListState(params: URLSearchParams): ListState {
	const filters: Filters = {};
	for (const key of TEXT_KEYS) {
		const v = params.get(key)?.trim();
		if (v && VALID[key](v)) filters[key] = v;
	}
	const type = params.get('object_type');
	if (type && (OBJECT_TYPES as readonly string[]).includes(type)) filters.object_type = type;
	const from = normalizeTime(params.get('from'));
	const to = normalizeTime(params.get('to'));
	if (from) filters.from = from;
	if (to) filters.to = to;

	const paging = parsePaging(params);
	const query: ListChangeLogQuery = { limit: paging.limit };
	if (filters.actor) query.actor = filters.actor;
	if (filters.object_type) query.object_type = filters.object_type as ObjectType;
	if (filters.object_id) query.object_id = filters.object_id;
	if (filters.action) query.action = filters.action;
	if (filters.from) query.from = filters.from;
	if (filters.to) query.to = filters.to;
	if (paging.cursor) query.cursor = paging.cursor;
	return { filters, paging, problem: rangeProblem(filters.from, filters.to), query };
}

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
	const v = filters[key] ?? '';
	if (key === 'object_type') return objectTypeLabels[v as ObjectType] ?? v;
	if (key === 'from' || key === 'to') return timeChip(v);
	return v;
}

export function filtersFromForm(form: FormData): { filters: Filters; limit: number | null } {
	const params = new URLSearchParams();
	for (const key of [...TEXT_KEYS, 'object_type'] as const) {
		const v = form.get(key);
		if (typeof v === 'string' && v.trim()) params.set(key, v.trim());
	}
	for (const key of ['from', 'to'] as const) {
		const v = normalizeTime(String(form.get(key) ?? ''));
		if (v) params.set(key, v);
	}
	const limit = Number(form.get('limit'));
	return {
		filters: parseListState(params).filters,
		limit: Number.isFinite(limit) && limit > 0 ? limit : null
	};
}

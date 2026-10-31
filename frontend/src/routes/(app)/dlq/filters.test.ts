import { describe, expect, it } from 'vitest';
import {
	chipValue,
	filterFields,
	filtersFromFields,
	filtersFromForm,
	isFiltered,
	parseListState,
	rangeProblem,
	searchFor,
	toDlqFilter,
	without
} from './filters';

// HUB-266: DLQ filters <-> URL <-> listDeadLetters query and DlqFilter (HUB-267).

const q = (s: string) => new URLSearchParams(s);

describe('parseListState', () => {
	it.each([
		{ s: '', filters: {}, query: { limit: 50 } },
		{
			s: 'tenant_id=orders-service&integration_id=int_evt_brandA&reason=UPSTREAM_REJECTED&replayed=false',
			filters: {
				tenant_id: 'orders-service',
				integration_id: 'int_evt_brandA',
				reason: 'UPSTREAM_REJECTED',
				replayed: 'false'
			},
			query: {
				tenant_id: 'orders-service',
				integration_id: 'int_evt_brandA',
				reason: 'UPSTREAM_REJECTED',
				replayed: false,
				limit: 50
			}
		},
		{
			s: 'vendor_id=clevertap&account_id=ct-brandA-in&reason=poison&cursor=c1&limit=200',
			filters: { vendor_id: 'clevertap', account_id: 'ct-brandA-in', reason: 'poison' },
			query: {
				vendor_id: 'clevertap',
				account_id: 'ct-brandA-in',
				reason: 'poison',
				cursor: 'c1',
				limit: 200
			}
		},
		{
			s: 'from=2026-10-31T00:00&to=2026-10-31T12:00:00Z',
			filters: { from: '2026-10-31T00:00:00Z', to: '2026-10-31T12:00:00Z' },
			query: { from: '2026-10-31T00:00:00Z', to: '2026-10-31T12:00:00Z', limit: 50 }
		},
		// Invalid values are dropped, not sent.
		{
			s: 'tenant_id=a&reason=not%20a%20code&replayed=maybe&from=yesterday',
			filters: {},
			query: { limit: 50 }
		}
	])('$s', ({ s, filters, query }) => {
		const st = parseListState(q(s));
		expect(st.filters).toEqual(filters);
		expect(st.query).toEqual(query);
		expect(st.problem).toBeNull();
	});

	it('refuses a reversed range before calling the API', () => {
		const st = parseListState(q('from=2026-10-31T12:00&to=2026-10-31T09:00'));
		expect(st.problem).toBe('"To" must be later than "From".');
		expect(rangeProblem('2026-10-31T09:00:00Z', '2026-11-14T09:00:00Z')).toBeNull();
	});
});

describe('URL round trip', () => {
	it('writes filters in a stable order, paging after them', () => {
		const st = parseListState(q('replayed=true&account_id=ct-brandA-in&cursor=c2&prev=&limit=25'));
		const s = searchFor(st.filters, st.paging);
		expect(s).toBe('?account_id=ct-brandA-in&replayed=true&cursor=c2&prev=&limit=25');
		expect(parseListState(q(s.slice(1)))).toEqual(st);
	});

	it('removing a chip keeps the other filters', () => {
		const st = parseListState(q('tenant_id=orders-service&replayed=false'));
		expect(searchFor(without(st.filters, 'replayed'))).toBe('?tenant_id=orders-service');
		expect(isFiltered(without(without(st.filters, 'replayed'), 'tenant_id'))).toBe(false);
	});

	it('reads the form: empty fields dropped, reason upper-cased (poison kept)', () => {
		const f = new FormData();
		f.set('integration_id', ' int_evt_brandA ');
		f.set('reason', 'upstream_rejected');
		f.set('replayed', '');
		f.set('from', '2026-10-31T09:00');
		f.set('limit', '100');
		expect(filtersFromForm(f)).toEqual({
			filters: {
				integration_id: 'int_evt_brandA',
				reason: 'UPSTREAM_REJECTED',
				from: '2026-10-31T09:00:00Z'
			},
			limit: 100
		});
		const g = new FormData();
		g.set('reason', 'Poison');
		expect(filtersFromForm(g).filters).toEqual({ reason: 'poison' });
	});

	it.each([
		['replayed', { replayed: 'true' as const }, 'Already replayed'],
		['replayed', { replayed: 'false' as const }, 'Not replayed yet'],
		['from', { from: '2026-10-31T09:00:00Z' }, '2026-10-31 09:00 UTC'],
		['reason', { reason: 'poison' }, 'poison']
	] as const)('chip %s', (key, filters, text) => expect(chipValue(key, filters)).toBe(text));
});

describe('bulk-action filter (HUB-267)', () => {
	it('maps the URL filters to DlqFilter', () => {
		const st = parseListState(
			q(
				'integration_id=int_evt_brandA&reason=UPSTREAM_REJECTED&replayed=false&from=2026-10-31T00:00'
			)
		);
		expect(toDlqFilter(st.filters)).toEqual({
			integration_id: 'int_evt_brandA',
			reason: 'UPSTREAM_REJECTED',
			replayed: false,
			from: '2026-10-31T00:00:00Z'
		});
		expect(toDlqFilter({})).toEqual({});
	});

	it('carries the filters through hidden fields and validates them again', () => {
		const filters = { account_id: 'ct-brandA-in', replayed: 'false' as const };
		const f = new FormData();
		for (const [k, v] of filterFields(filters)) f.set(k, v);
		expect([...f.keys()]).toEqual(['filter.account_id', 'filter.replayed']);
		// The change reason shares the name `reason` with the filter: it must not leak in.
		f.set('reason', 'Fixed T1 event-name sanitising');
		f.set('filter.tenant_id', 'x');
		expect(filtersFromFields(f)).toEqual(filters);
	});
});

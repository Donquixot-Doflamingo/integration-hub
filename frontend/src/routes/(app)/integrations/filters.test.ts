import { describe, expect, it } from 'vitest';
import {
	chipValue,
	conflictingFilters,
	filtersFromForm,
	isFiltered,
	parseListState,
	searchFor,
	without
} from './filters';

const q = (s: string) => new URLSearchParams(s);

describe('parseListState', () => {
	it.each([
		{ s: '', filters: {}, query: { limit: 50 } },
		{
			s: 'q=brand&tenant_id=orders-service&capability=events.track@v1&paused=true',
			filters: {
				q: 'brand',
				tenant_id: 'orders-service',
				capability: 'events.track@v1',
				paused: 'true'
			},
			query: {
				q: 'brand',
				tenant_id: 'orders-service',
				capability: 'events.track@v1',
				paused: true,
				limit: 50
			}
		},
		{ s: 'paused=false', filters: { paused: 'false' }, query: { paused: false, limit: 50 } },
		{ s: 'paused=maybe&q=%20%20', filters: {}, query: { limit: 50 } },
		{
			s: 'vendor_id=clevertap&account_id=ct-brandA-in&cursor=c1&limit=200',
			filters: { vendor_id: 'clevertap', account_id: 'ct-brandA-in' },
			query: { vendor_id: 'clevertap', account_id: 'ct-brandA-in', cursor: 'c1', limit: 200 }
		},
		{ s: `q=${'a'.repeat(129)}`, filters: {}, query: { limit: 50 } },
		// Active-version filters (hub-admin 1.1.0, HUB-283).
		{
			s: 'has_active_version=false',
			filters: { has_active_version: 'false' },
			query: { has_active_version: false, limit: 50 }
		},
		{
			s: 'has_active_version=true&active_version_behind=true',
			filters: { has_active_version: 'true', active_version_behind: 'true' },
			query: { has_active_version: true, active_version_behind: true, limit: 50 }
		},
		{
			s: 'active_version_behind=false',
			filters: { active_version_behind: 'false' },
			query: { active_version_behind: false, limit: 50 }
		},
		{ s: 'has_active_version=yes&active_version_behind=1', filters: {}, query: { limit: 50 } }
	])('$s', ({ s, filters, query }) => {
		const st = parseListState(q(s));
		expect(st.filters).toEqual(filters);
		expect(st.query).toEqual(query);
	});
});

describe('URL round trip', () => {
	it('writes filters in a stable order and paging after them', () => {
		const st = parseListState(q('paused=true&tenant_id=a-b&cursor=c2&prev=&prev=c1&limit=25'));
		expect(searchFor(st.filters, st.paging)).toBe(
			'?tenant_id=a-b&paused=true&cursor=c2&prev=&prev=c1&limit=25'
		);
		expect(parseListState(q(searchFor(st.filters, st.paging).slice(1)))).toEqual(st);
	});

	it('removing a filter keeps the others', () => {
		const st = parseListState(q('tenant_id=a-b&paused=true'));
		expect(searchFor(without(st.filters, 'paused'))).toBe('?tenant_id=a-b');
		expect(isFiltered(without(without(st.filters, 'paused'), 'tenant_id'))).toBe(false);
	});

	it('reads the filter form, dropping empty fields', () => {
		const f = new FormData();
		f.set('q', '  brand ');
		f.set('tenant_id', '');
		f.set('paused', 'false');
		f.set('limit', '100');
		expect(filtersFromForm(f)).toEqual({ filters: { q: 'brand', paused: 'false' }, limit: 100 });
	});
});

describe('active-version filters (HUB-283)', () => {
	it('round-trips through the URL after the other filters', () => {
		const st = parseListState(q('active_version_behind=true&paused=false&has_active_version=true'));
		expect(searchFor(st.filters)).toBe(
			'?paused=false&has_active_version=true&active_version_behind=true'
		);
	});

	it.each([
		['has_active_version', 'true', 'Published'],
		['has_active_version', 'false', 'None (never published)'],
		['active_version_behind', 'true', 'Waiting'],
		['active_version_behind', 'false', 'None waiting']
	] as const)('chip %s=%s reads "%s"', (key, value, text) => {
		expect(chipValue(key, value)).toBe(text);
	});

	it('flags "no active version" combined with "newer version" (matches nothing)', () => {
		expect(
			conflictingFilters({ has_active_version: 'false', active_version_behind: 'true' })
		).toMatch(/match nothing/);
		expect(
			conflictingFilters({ has_active_version: 'false', active_version_behind: 'false' })
		).toMatch(/match nothing/);
		expect(
			conflictingFilters({ has_active_version: 'true', active_version_behind: 'true' })
		).toBeNull();
		expect(conflictingFilters({ has_active_version: 'false' })).toBeNull();
	});
});

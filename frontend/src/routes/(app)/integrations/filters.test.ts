import { describe, expect, it } from 'vitest';
import { filtersFromForm, isFiltered, parseListState, searchFor, without } from './filters';

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
		{ s: `q=${'a'.repeat(129)}`, filters: {}, query: { limit: 50 } }
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

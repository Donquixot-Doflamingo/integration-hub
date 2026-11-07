import { describe, expect, it } from 'vitest';
import { chipValue, filtersFromForm, parseListState, searchFor, without } from './filters';

// HUB-268: transformers list filters <-> URL <-> listTransformers query.

const q = (s: string) => new URLSearchParams(s);

describe('parseListState', () => {
	it.each([
		{ s: '', filters: {}, query: { limit: 50 } },
		{
			s: 'q=T1&kind=request&vendor_id=clevertap&cursor=c1&limit=25',
			filters: { q: 'T1', kind: 'request', vendor_id: 'clevertap' },
			query: {
				q: 'T1',
				kind: 'request',
				vendor_id: 'clevertap',
				cursor: 'c1',
				limit: 25
			}
		},
		{ s: 'kind=sideways&vendor_id=x', filters: {}, query: { limit: 50 } }
	])('$s', ({ s, filters, query }) => {
		const st = parseListState(q(s));
		expect(st.filters).toEqual(filters);
		expect(st.query).toEqual(query);
	});

	it('round-trips and removes one filter', () => {
		const st = parseListState(q('vendor_id=clevertap&kind=response'));
		expect(searchFor(st.filters)).toBe('?kind=response&vendor_id=clevertap');
		expect(searchFor(without(st.filters, 'kind'))).toBe('?vendor_id=clevertap');
		expect(chipValue('kind', 'response')).toBe('Response (vendor response to caller)');
	});

	it('reads the form, dropping empty fields', () => {
		const f = new FormData();
		f.set('q', ' T1 ');
		f.set('kind', '');
		f.set('limit', '100');
		expect(filtersFromForm(f)).toEqual({ filters: { q: 'T1' }, limit: 100 });
	});
});

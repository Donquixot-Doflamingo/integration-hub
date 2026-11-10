import { describe, expect, it } from 'vitest';
import {
	chipValue,
	filtersFromForm,
	isFiltered,
	parseListState,
	searchFor,
	without
} from './filters';

// HUB-274: change log filters <-> URL <-> listChangeLog query.

describe('parseListState', () => {
	it('maps every filter to the contract query names', () => {
		const s = parseListState(
			new URLSearchParams(
				'actor=priya@example.com&object_type=integration&object_id=int_evt_brandA&action=publishIntegrationVersion&from=2026-10-31T00:00:00Z&to=2026-11-01T00:00:00Z&limit=25&cursor=abc'
			)
		);
		expect(s.query).toEqual({
			actor: 'priya@example.com',
			object_type: 'integration',
			object_id: 'int_evt_brandA',
			action: 'publishIntegrationVersion',
			from: '2026-10-31T00:00:00Z',
			to: '2026-11-01T00:00:00Z',
			limit: 25,
			cursor: 'abc'
		});
		expect(s.problem).toBeNull();
	});

	it.each([
		['object_type=nonsense', {}],
		['action=DROP TABLE', {}],
		['action=publishIntegrationVersion', { action: 'publishIntegrationVersion' }],
		['from=yesterday', {}],
		['object_id=' + 'x'.repeat(129), {}]
	])('drops invalid values: %s', (q, want) => {
		expect(parseListState(new URLSearchParams(q)).filters).toEqual(want);
	});

	it('refuses a reversed range', () => {
		expect(
			parseListState(new URLSearchParams('from=2026-11-01T00:00:00Z&to=2026-10-31T00:00:00Z'))
				.problem
		).toBe('"To" must be later than "From".');
	});
});

it('round-trips filters through the URL in a stable order', () => {
	const f = { object_id: 'int_evt_brandA', actor: 'priya@example.com' };
	expect(searchFor(f)).toBe('?actor=priya%40example.com&object_id=int_evt_brandA');
	expect(parseListState(new URLSearchParams(searchFor(f))).filters).toEqual(f);
	expect(searchFor({})).toBe('');
	expect(isFiltered(f)).toBe(true);
	expect(without(f, 'actor')).toEqual({ object_id: 'int_evt_brandA' });
});

it('chips read like the form', () => {
	const f = { object_type: 'registrar_endpoint', from: '2026-10-31T09:00:00Z' };
	expect(chipValue('object_type', f)).toBe('Callback endpoint');
	expect(chipValue('from', f)).toBe('2026-10-31 09:00 UTC');
});

it('reads the form (datetime-local as UTC, empty fields dropped)', () => {
	const form = new FormData();
	form.set('actor', ' priya@example.com ');
	form.set('object_id', '');
	form.set('object_type', 'tenant');
	form.set('from', '2026-10-31T09:00');
	form.set('limit', '100');
	expect(filtersFromForm(form)).toEqual({
		filters: { actor: 'priya@example.com', object_type: 'tenant', from: '2026-10-31T09:00:00Z' },
		problems: {},
		limit: 100
	});
});

it('names the fields whose text was dropped (HUB-455)', () => {
	const form = new FormData();
	form.set('action', 'Not An Operation');
	form.set('actor', 'priya@example.com');
	const r = filtersFromForm(form);
	expect(Object.keys(r.problems)).toEqual(['action']);
	expect(r.filters).toEqual({ actor: 'priya@example.com' });
});

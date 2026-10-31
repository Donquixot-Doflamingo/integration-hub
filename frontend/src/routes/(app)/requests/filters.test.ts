import { describe, expect, it } from 'vitest';
import {
	filtersFromForm,
	lastRange,
	normalizeTime,
	parseSearch,
	rangeProblem,
	searchFor,
	toLocalInput
} from './filters';

// HUB-263: request explorer search <-> URL.

const parse = (qs: string) => parseSearch(new URLSearchParams(qs));

describe('parseSearch', () => {
	it('maps every filter to the searchRequests query', () => {
		const s = parse(
			'tenant_id=orders-service&integration_id=int_evt_brandA&status=dead_lettered,parked&code=UPSTREAM_TIMEOUT&vendor_id=clevertap&account_id=ct-brandA-in&idempotency_key=order-8821-created&from=2026-10-31T00:00:00Z&to=2026-11-01T00:00:00Z&cursor=abc&limit=100'
		);
		expect(s.problem).toBeNull();
		expect(s.query).toEqual({
			tenant_id: 'orders-service',
			integration_id: 'int_evt_brandA',
			status: ['dead_lettered', 'parked'],
			code: ['UPSTREAM_TIMEOUT'],
			vendor_id: 'clevertap',
			account_id: 'ct-brandA-in',
			idempotency_key: 'order-8821-created',
			from: '2026-10-31T00:00:00Z',
			to: '2026-11-01T00:00:00Z',
			cursor: 'abc',
			limit: 100
		});
	});

	it('drops values the contract would reject', () => {
		const s = parse('tenant_id=a b&status=nope,parked,parked&code=lower,OK_CODE&from=yesterday');
		expect(s.filters).toEqual({ status: ['parked'], code: ['OK_CODE'] });
		expect(s.query).toEqual({ status: ['parked'], code: ['OK_CODE'], limit: 50 });
	});

	it.each([
		[
			'from=2026-10-01T00:00:00Z&to=2026-11-02T00:00:00Z',
			'The time range can span at most 31 days.'
		],
		['from=2026-11-01T00:00:00Z&to=2026-10-31T00:00:00Z', '"To" must be later than "From".'],
		['from=2026-10-01T00:00:00Z&to=2026-11-01T00:00:00Z', null],
		['from=2026-10-01T00:00:00Z', null]
	])('%s -> %s', (qs, problem) => expect(parse(qs).problem).toBe(problem));
});

describe('times', () => {
	it.each([
		['2026-10-31T09:00', '2026-10-31T09:00:00Z'],
		['2026-10-31T09:00:30Z', '2026-10-31T09:00:30Z'],
		['2026-10-31T09:00:30.123Z', '2026-10-31T09:00:30Z'],
		['2026-02-31T09:00', null],
		['2026-10-31', null],
		['', null]
	])('normalizeTime(%j) = %j', (raw, want) => expect(normalizeTime(raw)).toBe(want));

	it('toLocalInput', () => expect(toLocalInput('2026-10-31T09:00:00Z')).toBe('2026-10-31T09:00'));

	it('lastRange ends at the next full minute', () => {
		const now = Date.parse('2026-10-31T09:15:20Z');
		expect(lastRange(3_600_000, now)).toEqual({
			from: '2026-10-31T08:16:00Z',
			to: '2026-10-31T09:16:00Z'
		});
		expect(
			rangeProblem(lastRange(7 * 86_400_000, now).from, lastRange(7 * 86_400_000, now).to)
		).toBeNull();
	});
});

describe('searchFor / filtersFromForm', () => {
	it('round-trips through the URL in a stable order', () => {
		const s = parse('status=parked,dead_lettered&tenant_id=orders-service&to=2026-11-01T00:00:00Z');
		expect(searchFor(s.filters)).toBe(
			'?tenant_id=orders-service&status=parked%2Cdead_lettered&to=2026-11-01T00%3A00%3A00Z'
		);
		expect(parse(searchFor(s.filters).slice(1)).filters).toEqual(s.filters);
	});

	it('reads the form: checkboxes, comma or space separated codes, UTC datetime-local', () => {
		const f = new FormData();
		f.set('integration_id', ' int_evt_brandA ');
		f.append('status', 'parked');
		f.append('status', 'failed');
		f.set('code', 'upstream_timeout, UPSTREAM_REJECTED');
		f.set('from', '2026-10-31T09:00');
		f.set('to', '');
		f.set('tenant_id', '');
		f.set('limit', '25');
		expect(filtersFromForm(f)).toEqual({
			filters: {
				integration_id: 'int_evt_brandA',
				status: ['parked', 'failed'],
				code: ['UPSTREAM_TIMEOUT', 'UPSTREAM_REJECTED'],
				from: '2026-10-31T09:00:00Z'
			},
			limit: 25
		});
	});
});

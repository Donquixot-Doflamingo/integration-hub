import { describe, expect, it } from 'vitest';
import { breadcrumbsFor, sectionFor, sections } from './nav';

describe('sections', () => {
	it('lists the ten console sections in order', () => {
		expect(sections.map((s) => s.label)).toEqual([
			'Integrations',
			'Health',
			'Requests',
			'DLQ',
			'Transformers',
			'Vendors',
			'Accounts',
			'Tenants',
			'Snapshots',
			'Change log'
		]);
	});
});

describe('sectionFor', () => {
	it.each([
		['/integrations', 'Integrations'],
		['/integrations/int_evt_brandA', 'Integrations'],
		['/change-log', 'Change log'],
		['/integrationsx', undefined],
		['/', undefined]
	])('%s -> %s', (path, label) => expect(sectionFor(path)?.label).toBe(label));
});

describe('breadcrumbsFor', () => {
	it.each([
		{ path: '/', want: ['Console'] },
		{ path: '/dlq', want: ['Console', 'DLQ'] },
		{
			path: '/integrations/int_evt_brandA/versions',
			want: ['Console', 'Integrations', 'int_evt_brandA', 'versions']
		},
		{
			path: '/integrations/int_evt_brandA/versions/8',
			want: ['Console', 'Integrations', 'int_evt_brandA', 'v8']
		},
		{
			path: '/vendors/clevertap/endpoints/upload@1',
			want: ['Console', 'Vendors', 'clevertap', 'upload@1']
		},
		{ path: '/vendors/new', want: ['Console', 'Vendors', 'New'] },
		{
			path: '/transformers/T1/new-version',
			want: ['Console', 'Transformers', 'T1', 'New version']
		},
		{
			path: '/integrations/int_evt_brandA/diff',
			want: ['Console', 'Integrations', 'int_evt_brandA', 'Compare']
		}
	])('$path', ({ path, want }) => {
		const crumbs = breadcrumbsFor(path);
		expect(crumbs.map((c) => c.label)).toEqual(want);
		expect(crumbs.at(-1)?.href).toBe(path === '/' ? '/' : path);
	});
});

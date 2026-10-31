import { describe, expect, it } from 'vitest';
import { appName, pageTitle } from './app-info';

describe('pageTitle', () => {
	it.each([
		{ section: undefined, want: appName },
		{ section: '', want: appName },
		{ section: '   ', want: appName },
		{ section: 'DLQ', want: `DLQ - ${appName}` },
		{ section: ' Health ', want: `Health - ${appName}` }
	])('pageTitle($section) = $want', ({ section, want }) => {
		expect(pageTitle(section)).toBe(want);
	});
});

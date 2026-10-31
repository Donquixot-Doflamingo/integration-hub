import { describe, expect, it } from 'vitest';
import { isThemePreference, resolveTheme } from './theme.svelte';

describe('theme', () => {
	it.each([
		['system', true, 'dark'],
		['system', false, 'light'],
		['light', true, 'light'],
		['dark', false, 'dark']
	] as const)('resolveTheme(%s, prefersDark=%s) = %s', (pref, dark, want) => {
		expect(resolveTheme(pref, dark)).toBe(want);
	});

	it.each([
		['dark', true],
		['system', true],
		['blue', false],
		[null, false]
	])('isThemePreference(%s) = %s', (v, want) => expect(isThemePreference(v)).toBe(want));
});

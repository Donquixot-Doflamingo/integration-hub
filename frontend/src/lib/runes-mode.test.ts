import { compile } from 'svelte/compiler';
import { describe, expect, it } from 'vitest';
import config from '../../svelte.config.js';

// HUB-250: runes mode is enforced for every project component (not for node_modules).
const runes = config.compilerOptions?.runes;

describe('svelte.config runes mode', () => {
	it('is a per-file function', () => {
		expect(typeof runes).toBe('function');
	});

	it.each([
		{ filename: '/repo/frontend/src/routes/+page.svelte', want: true },
		{ filename: 'C:\\repo\\frontend\\src\\lib\\components\\X.svelte', want: true },
		{ filename: '/repo/frontend/node_modules/some-lib/Button.svelte', want: undefined }
	])('$filename -> $want', ({ filename, want }) => {
		const fn = runes as (o: { filename: string }) => boolean | undefined;
		expect(fn({ filename })).toBe(want);
	});

	it('rejects Svelte 4 syntax in project files', () => {
		const legacy = '<script>export let x = 1;</script><p>{x}</p>';
		expect(() => compile(legacy, { filename: 'src/routes/Legacy.svelte', runes: true })).toThrow(
			/runes mode/
		);
	});
});

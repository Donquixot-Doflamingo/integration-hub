import { sveltekit } from '@sveltejs/kit/vite';
import { svelteTesting } from '@testing-library/svelte/vite';
import { msw } from 'msw/vite';
import { createRequire } from 'node:module';
import { defineConfig } from 'vitest/config';

/**
 * Mock mode (`npm run dev:mock`, `npm run build:mock`): the console runs on MSW handlers built from
 * the contract examples, with no backend. Off by default; production builds contain no mock code.
 */
const apiMocking = process.env.HUB_API_MOCKING === 'true';

// @testing-library/svelte resolves the `browser` export condition so Svelte mounts in jsdom. MSW's
// Node interceptors export `browser: null`, so point those subpaths at their Node files explicitly.
const require = createRequire(import.meta.url);
const nodeOnly = ['ClientRequest', 'http', 'net', 'presets/node'].map((sub) => ({
	find: `@mswjs/interceptors/${sub}`,
	replacement: require.resolve(`@mswjs/interceptors/${sub}`)
}));

export default defineConfig({
	plugins: [sveltekit(), svelteTesting(), apiMocking && msw({ mode: 'worker-only' })],
	define: {
		__API_MOCKING__: JSON.stringify(apiMocking)
	},
	test: {
		environment: 'jsdom',
		include: ['src/**/*.{test,spec}.{js,ts}', 'scripts/**/*.{test,spec}.{js,ts}'],
		setupFiles: ['./src/vitest.setup.ts'],
		restoreMocks: true,
		alias: nodeOnly,
		server: { deps: { inline: ['msw', /@mswjs\/interceptors/] } }
	}
});

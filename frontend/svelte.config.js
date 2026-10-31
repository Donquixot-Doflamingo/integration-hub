import adapter from '@sveltejs/adapter-node';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
const config = {
	preprocess: vitePreprocess(),
	compilerOptions: {
		// Runes mode is mandatory for every component in this project (Svelte 5 only, no legacy
		// `export let` / `$:` syntax). Third-party packages in node_modules keep their own mode.
		runes: ({ filename }) => (filename.split(/[/\\]/).includes('node_modules') ? undefined : true)
	},
	kit: {
		// Internal console: SSR on a Node server (form actions, httpOnly session cookie).
		adapter: adapter(),
		alias: {
			$components: 'src/lib/components'
		}
	}
};

export default config;

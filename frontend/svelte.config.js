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
		},
		// Content-Security-Policy (HUB-499, gap G12, SECURITY §2.2). Scripts: self plus the nonces
		// SvelteKit puts on its own inline bootstrap and on app.html's theme script
		// (%sveltekit.nonce%); never 'unsafe-inline'. `connect-src` and `form-action` also get the
		// ISFC auth origin at request time (src/lib/server/csp.ts), because it is runtime config.
		// `style-src-attr` allows style="" attributes only (server-rendered markup such as
		// `display: contents`); style elements stay nonce or file based.
		csp: {
			mode: 'auto',
			directives: {
				'default-src': ['self'],
				'script-src': ['self'],
				'style-src': ['self'],
				'style-src-attr': ['unsafe-inline'],
				'img-src': ['self', 'data:'],
				'font-src': ['self'],
				'connect-src': ['self'],
				'form-action': ['self'],
				'worker-src': ['self'],
				'frame-ancestors': ['none'],
				'object-src': ['none'],
				'base-uri': ['self']
			}
		}
	}
};

export default config;

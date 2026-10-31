// See https://svelte.dev/docs/kit/types#app.d.ts
declare global {
	namespace App {
		interface Error {
			message: string;
			code?: string;
			requestId?: string | null;
		}
		// interface Locals {}
		// interface PageData {}
		// interface PageState {}
		// interface Platform {}
	}
}

export {};

declare global {
	/** True in mock mode (`HUB_API_MOCKING=true`, set by `npm run dev:mock` / `build:mock`). Replaced at build time. */
	const __API_MOCKING__: boolean;
}

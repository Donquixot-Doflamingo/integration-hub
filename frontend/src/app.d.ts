// See https://svelte.dev/docs/kit/types#app.d.ts
import type { Session } from '$lib/server/auth/session';

declare global {
	namespace App {
		interface Error {
			message: string;
			code?: string;
			requestId?: string | null;
		}
		interface Locals {
			/** Verified console session (HUB-253), or null when signed out. Server only. */
			session: Session | null;
			/** True when a session cookie came in but did not verify or had expired (HUB-492). */
			sessionEnded: boolean;
			/** The session cookie was rewritten during this request (refresh, tenant, end). */
			sessionDirty?: boolean;
		}
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

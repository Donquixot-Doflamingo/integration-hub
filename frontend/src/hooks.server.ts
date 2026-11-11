import type { HandleFetch, ServerInit } from '@sveltejs/kit';
import { sequence } from '@sveltejs/kit/hooks';
import { proxyAdminApi, serializeAdminHeaders } from '$lib/server/admin-proxy';
import { assertAuthModeAtStartup } from '$lib/server/auth';
import { attachAdminToken, authenticate } from '$lib/server/auth/hooks';
import { rejectDoubleSlashPaths } from '$lib/server/security';

// Order matters: path hygiene first (ADR-0009), then the session (HUB-253), then the same-origin
// admin API proxy, which needs the session's token (HUB-286, ADR-0022; a no-op unless
// HUB_ADMIN_UPSTREAM is set), then the SSR header list for admin responses (HUB-289).
// The login redirect itself lives in routes/(app)/+layout.server.ts so it works for SSR and for
// client-side navigations (data requests) alike.
export const handle = sequence(
	rejectDoubleSlashPaths,
	authenticate,
	proxyAdminApi,
	serializeAdminHeaders
);

// A deployed console refuses to start with the dev role picker (HUB-495).
export const init: ServerInit = () => assertAuthModeAtStartup();

export const handleFetch: HandleFetch = async (input) => {
	if (__API_MOCKING__) {
		// Mock builds only (constant-folded away otherwise): form actions call the admin API from the
		// server, where the browser's MSW worker cannot answer, so the same mocks answer here.
		const { mockAdminFetch } = await import('$lib/api/mocks/server-fetch');
		const mocked = await mockAdminFetch(input.event, input.request);
		if (mocked) return mocked;
	}
	return attachAdminToken(input);
};

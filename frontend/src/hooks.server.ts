import type { HandleFetch } from '@sveltejs/kit';
import { sequence } from '@sveltejs/kit/hooks';
import { attachAdminToken, authenticate } from '$lib/server/auth/hooks';
import { rejectDoubleSlashPaths } from '$lib/server/security';

// Order matters: path hygiene first (ADR-0009), then the session (HUB-253).
// The login redirect itself lives in routes/(app)/+layout.server.ts so it works for SSR and for
// client-side navigations (data requests) alike.
export const handle = sequence(rejectDoubleSlashPaths, authenticate);

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

import { sequence } from '@sveltejs/kit/hooks';
import { attachAdminToken, authenticate } from '$lib/server/auth/hooks';
import { rejectDoubleSlashPaths } from '$lib/server/security';

// Order matters: path hygiene first (ADR-0009), then the session (HUB-253).
// The login redirect itself lives in routes/(app)/+layout.server.ts so it works for SSR and for
// client-side navigations (data requests) alike.
export const handle = sequence(rejectDoubleSlashPaths, authenticate);

export const handleFetch = attachAdminToken;

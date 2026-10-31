import { sequence } from '@sveltejs/kit/hooks';
import { rejectDoubleSlashPaths } from '$lib/server/security';

// Order matters: path hygiene first (ADR-0009), before any route or session work.
export const handle = sequence(rejectDoubleSlashPaths);

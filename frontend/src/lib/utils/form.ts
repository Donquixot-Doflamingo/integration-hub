import type { FieldErrors } from '$lib/api/actions';
import { pointerToPath } from './diff';

/**
 * Field problems (keyed by JSON pointer, HUB-290+) as the list `AuthoringForm` shows when a submit
 * is refused: `/defaults/breaker/window_ms` reads `defaults.breaker.window_ms`.
 */
export function blockersFrom(found: FieldErrors): { label: string; message: string }[] {
	return Object.entries(found)
		.filter((e): e is [string, string] => !!e[1])
		.map(([ptr, message]) => ({
			label: ptr.startsWith('/') ? pointerToPath(ptr, 'form') : ptr,
			message
		}));
}

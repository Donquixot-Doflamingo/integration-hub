/**
 * A success notice that survives one client-side navigation (HUB-290+): a create page sets it, then
 * navigates to the new object's page, which takes it and shows it as a `ChangeNotice` (what was
 * done plus the change log id). Kept in memory only: never in the URL or web storage, gone on
 * reload. It never carries a secret.
 */
export interface Flash {
	message: string;
	changeId: string | null;
	requestId: string | null;
	link?: { href: string; label: string };
}

const state = $state<{ pending: Flash | null }>({ pending: null });

export function setFlash(flash: Flash): void {
	state.pending = flash;
}

/** Returns the pending notice (once) and clears it. */
export function takeFlash(): Flash | null {
	const f = state.pending;
	state.pending = null;
	return f;
}

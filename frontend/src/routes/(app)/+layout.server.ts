import { redirect } from '@sveltejs/kit';
import { env as publicEnv } from '$env/dynamic/public';
import { loginUrl } from '$lib/auth/redirect';
import type { SwitchTarget } from '$lib/auth/tenant-choice';
import { authMode } from '$lib/server/auth';
import { loadSwitchTargets } from '$lib/server/auth/panels';
import { toPublicSession } from '$lib/server/auth/session';
import type { LayoutServerLoad } from './$types';

/** `PUBLIC_ISFC_ADMIN_URL` when it is an http(s) URL: where operators change ISFC-owned data. */
function isfcAdminUrl(): string | null {
	const raw = publicEnv.PUBLIC_ISFC_ADMIN_URL?.trim();
	if (!raw) return null;
	try {
		const u = new URL(raw);
		return u.protocol === 'https:' || u.protocol === 'http:' ? u.toString() : null;
	} catch {
		return null;
	}
}

/** What the tenant switcher shows (ISFC sessions only; HUB-494). */
export interface SwitcherData {
	choices: SwitchTarget[];
	/** The tenant in use: the session's choice, else ISFC's default (the first, top tenant). */
	currentId: string | null;
	/** Why the list is empty, when it could not be loaded. */
	error: string | null;
}

// Every console page requires a session (HUB-253). Unauthenticated users go to /login and come
// back to the page they asked for. Only the token-free view of the session reaches the browser.
// `isfcMode` tells pages that tenants come from ISFC (HUB-485): they hide "Create tenant".
export const load: LayoutServerLoad = async (event) => {
	const { locals, url } = event;
	// A cookie that no longer verified (expired, or the key changed) says so on the login page.
	if (!locals.session) redirect(303, loginUrl(url, { expired: locals.sessionEnded }));
	const session = locals.session;
	let tenantSwitcher: SwitcherData | null = null;
	if (session.provider === 'isfc') {
		const r = await loadSwitchTargets(event);
		const choices = r.ok ? r.choices : [];
		const chosen = choices.find((c) => c.id === session.tenantId);
		tenantSwitcher = {
			choices,
			currentId: chosen?.id ?? choices[0]?.id ?? null,
			error: r.ok ? null : r.message
		};
	}
	return {
		session: toPublicSession(locals.session ?? session),
		isfcMode: authMode() === 'isfc',
		isfcAdminUrl: isfcAdminUrl(),
		tenantSwitcher
	};
};

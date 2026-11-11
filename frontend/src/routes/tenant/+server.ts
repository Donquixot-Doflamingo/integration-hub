import { json, redirect } from '@sveltejs/kit';
import { safeNext } from '$lib/auth/redirect';
import { authMode, sessionSecret } from '$lib/server/auth';
import { writeSessionCookie } from '$lib/server/auth/cookie';
import { loadSwitchTargets } from '$lib/server/auth/panels';
import type { RequestHandler } from './$types';

/**
 * Tenant switch (HUB-494). The choice is kept in the encrypted session; the admin proxy sends it
 * as `X-Tenant-Id`. The tenant must be one ISFC lists for this user (checked again here against
 * `GET /oauth2/panels`: the browser's word is not enough). A plain form post answers 303 back to
 * the page, the console's script posts it with `Accept: application/json` and then reloads the
 * page data (caches and `/me`).
 */
export const POST: RequestHandler = async (event) => {
	const { request, locals, cookies, url } = event;
	const wantsJson = request.headers.get('accept')?.includes('application/json') ?? false;
	const session = locals.session;
	const secret = sessionSecret();
	if (authMode() !== 'isfc' || !session || session.provider !== 'isfc' || !secret) {
		if (wantsJson) return json({ ok: false, message: 'Not signed in.' }, { status: 401 });
		redirect(303, '/login');
	}
	const form = await request.formData();
	const tenantId = String(form.get('tenant_id') ?? '').toLowerCase();
	const back = safeNext(String(form.get('next') ?? ''));
	const choices = await loadSwitchTargets(event);
	if (!choices.ok) {
		if (wantsJson) return json({ ok: false, message: choices.message }, { status: 503 });
		redirect(303, back);
	}
	if (!choices.choices.some((c) => c.id === tenantId)) {
		const message = 'You have no access to that tenant. Pick one from the list.';
		if (wantsJson) return json({ ok: false, message }, { status: 403 });
		redirect(303, back);
	}
	const next = { ...locals.session!, tenantId };
	locals.session = next;
	writeSessionCookie(cookies, next, secret, url.protocol === 'https:');
	if (wantsJson) return json({ ok: true, tenantId });
	redirect(303, back);
};

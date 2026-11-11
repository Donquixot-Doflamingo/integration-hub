import type { RequestEvent } from '@sveltejs/kit';
import { isTenantUuid, type SwitchTarget } from '$lib/auth/tenant-choice';
import type { IsfcConfig } from './config';
import { IsfcError, isfcCall, type Fetch } from './isfc';
import { refreshForEvent } from './refresh';
import { readIsfcConfig } from './config';

/**
 * The tenants the operator can work in (HUB-494, AUTH_PLAN §9): `GET {gateway}/auth/oauth2/panels`
 * lists the panels the user can open, each with the tenants the user may enter there. The
 * console's own panel gives the tenant switcher; the platform tenant is listed first. ISFC is the
 * judge: a tenant that is not in this list is never accepted, whatever the browser posts.
 */
interface PanelsBody {
	data?: {
		panels?: {
			client_id?: unknown;
			current?: unknown;
			tenants?: { id?: unknown; key?: unknown; name?: unknown; roles?: unknown }[] | null;
		}[];
	} | null;
}

const text = (v: unknown, fallback: string) => (typeof v === 'string' && v ? v : fallback);

/** Pure: this panel's tenants from the panels answer, the platform tenant first, order kept otherwise. */
export function tenantChoices(body: PanelsBody, config: IsfcConfig): SwitchTarget[] {
	const panels = body?.data?.panels ?? [];
	const mine =
		panels.find((p) => p.current === true) ?? panels.find((p) => p.client_id === config.clientId);
	const out: SwitchTarget[] = [];
	for (const t of mine?.tenants ?? []) {
		const id = typeof t.id === 'string' ? t.id.toLowerCase() : '';
		if (!isTenantUuid(id) || out.some((o) => o.id === id)) continue;
		out.push({
			id,
			key: text(t.key, id),
			name: text(t.name, text(t.key, id)),
			platform: id === config.platformTenantId
		});
	}
	return out.sort((a, b) => Number(b.platform) - Number(a.platform));
}

/** `GET {gateway}/auth/oauth2/panels` with the access token. Throws `IsfcError`. */
export async function fetchSwitchTargets(
	config: IsfcConfig,
	fetchImpl: Fetch,
	input: { accessToken: string; deviceId: string | null; userAgent: string | null }
): Promise<SwitchTarget[]> {
	if (!config.gatewayUrl)
		throw new IsfcError(0, 'not_configured', 'HUB_ISFC_GATEWAY_URL is not set.');
	const { body } = await isfcCall<PanelsBody>(
		fetchImpl,
		`${config.gatewayUrl}/auth/oauth2/panels`,
		{
			method: 'GET',
			headers: { authorization: `Bearer ${input.accessToken}` },
			deviceId: input.deviceId ?? undefined,
			userAgent: input.userAgent
		}
	);
	return tenantChoices(body, config);
}

export type ChoicesResult = { ok: true; choices: SwitchTarget[] } | { ok: false; message: string };

type Ev = Pick<RequestEvent, 'cookies' | 'locals' | 'request' | 'url' | 'fetch'>;

/**
 * The tenant choices of the request's session. A gateway "token no longer valid" is refreshed once
 * and tried again. Never throws: a failure is a message for the switcher, not a broken page.
 */
export async function loadSwitchTargets(event: Ev): Promise<ChoicesResult> {
	const cfg = readIsfcConfig();
	if (!cfg.ok || !cfg.config.gatewayUrl) {
		return { ok: false, message: 'The tenant list is not configured (HUB_ISFC_GATEWAY_URL).' };
	}
	const call = () => {
		const s = event.locals.session;
		if (!s?.accessToken) throw new IsfcError(401, 'GW_001', 'no token');
		return fetchSwitchTargets(cfg.config, event.fetch, {
			accessToken: s.accessToken,
			deviceId: s.deviceId,
			userAgent: event.request.headers.get('user-agent')
		});
	};
	try {
		return { ok: true, choices: await call() };
	} catch (e) {
		if (e instanceof IsfcError && e.status === 401) {
			try {
				if ((await refreshForEvent(event, { fetch: event.fetch, force: true })) === 'refreshed') {
					return { ok: true, choices: await call() };
				}
			} catch {
				/* fall through to the message */
			}
		}
		return {
			ok: false,
			message: 'ISFC did not return your tenants. Reload the page to try again.'
		};
	}
}

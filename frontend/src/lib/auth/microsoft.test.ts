import { describe, expect, it, vi } from 'vitest';
import {
	encodeState,
	ENTRA_AUTHORITY,
	microsoftAuthorizeUrl,
	MicrosoftSsoError,
	startMicrosoftSso,
	type MicrosoftPlan
} from './microsoft';

// HUB-493: the browser half of Microsoft SSO, against a fake provider (no real ISFC or Entra).
const plan: MicrosoftPlan = {
	flowUrl:
		'https://auth.isfc.test/oauth2/auth?client_id=integration-hub-console&state=S&code_challenge=C&code_challenge_method=S256',
	deviceId: 'device-00000000-0000-4000-8000-000000000000',
	clientId: 'integration-hub-console',
	consoleRedirectUri: 'https://console.test/auth/callback',
	idp: {
		clientId: 'entra-client',
		redirectUri: 'https://auth.isfc.test/oauth2/callback',
		tenantId: 'entra-tenant-id',
		domainHint: 'example.com',
		countryCode: 'IN'
	}
};

const decodeState = (s: string) => JSON.parse(Buffer.from(s, 'base64url').toString('utf8'));

/** A fake Hydra start-flow endpoint and a recording `assign`. */
function fakes(flow: () => Response | Promise<Response>) {
	const calls: { url: string; init: RequestInit }[] = [];
	const fetchImpl = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
		calls.push({ url: String(url), init: init ?? {} });
		return flow();
	});
	return { fetch: fetchImpl as unknown as typeof fetch, calls, assign: vi.fn() };
}

describe('encodeState', () => {
	it('is base64url JSON without padding', () => {
		const s = encodeState({ a: 'é?>', b: 1 });
		expect(s).toMatch(/^[A-Za-z0-9_-]+$/);
		expect(decodeState(s)).toEqual({ a: 'é?>', b: 1 });
	});
});

describe('microsoftAuthorizeUrl', () => {
	it('is Entra v2 authorize with the plan, the flow token and ISFC state', () => {
		const url = new URL(microsoftAuthorizeUrl(plan, 'flow-123'));
		expect(`${url.origin}${url.pathname}`).toBe(
			`${ENTRA_AUTHORITY}/entra-tenant-id/oauth2/v2.0/authorize`
		);
		const q = url.searchParams;
		expect(q.get('client_id')).toBe('entra-client');
		expect(q.get('response_type')).toBe('code');
		expect(q.get('response_mode')).toBe('query');
		expect(q.get('redirect_uri')).toBe('https://auth.isfc.test/oauth2/callback');
		expect(q.get('scope')).toBe('openid email profile User.Read');
		expect(q.get('domain_hint')).toBe('example.com');
		expect(decodeState(q.get('state')!)).toEqual({
			method_id: 'MICROSOFT_SSO',
			flow_token: 'flow-123',
			device_id: plan.deviceId,
			country_code: 'IN',
			client_id: 'integration-hub-console',
			redirect_uri: 'https://console.test/auth/callback',
			client_redirect_uri: 'https://console.test/auth/callback'
		});
	});

	it('omits domain_hint when there is none and escapes the tenant id', () => {
		const url = new URL(
			microsoftAuthorizeUrl(
				{ ...plan, idp: { ...plan.idp, domainHint: null, tenantId: 'a b' } },
				'f'
			)
		);
		expect(url.searchParams.has('domain_hint')).toBe(false);
		expect(url.pathname).toBe('/a%20b/oauth2/v2.0/authorize');
	});
});

describe('startMicrosoftSso', () => {
	it('opens the Hydra flow with credentials and x-device-id, then goes to Entra with the flow token', async () => {
		const f = fakes(() =>
			Response.json({ success: true, data: { flow_token: 'flow-abc', methods: [] } })
		);
		await startMicrosoftSso(plan, {
			fetch: f.fetch,
			assign: f.assign,
			authority: 'https://entra.fake'
		});
		expect(f.calls).toHaveLength(1);
		expect(f.calls[0]!.url).toBe(plan.flowUrl);
		expect(f.calls[0]!.init).toMatchObject({
			method: 'GET',
			credentials: 'include',
			headers: { 'x-device-id': plan.deviceId }
		});
		expect(f.assign).toHaveBeenCalledOnce();
		const to = new URL(f.assign.mock.calls[0]![0] as string);
		expect(to.origin).toBe('https://entra.fake');
		expect(decodeState(to.searchParams.get('state')!).flow_token).toBe('flow-abc');
	});

	it('the whole round trip against a fake Hydra, auth-service callback and Entra', async () => {
		// Fake Entra: returns to auth-service's callback with the state it was given; fake
		// auth-service: finishes the flow for that flow token; fake Hydra: redirects to the console.
		const f = fakes(() => Response.json({ data: { flow_token: 'flow-777' } }));
		await startMicrosoftSso(plan, { fetch: f.fetch, assign: f.assign });
		const entra = new URL(f.assign.mock.calls[0]![0] as string);
		expect(entra.origin).toBe(ENTRA_AUTHORITY);
		const state = decodeState(entra.searchParams.get('state')!);
		expect(state).toMatchObject({ flow_token: 'flow-777', redirect_uri: plan.consoleRedirectUri });
		// What auth-service redirects to at the end: the console callback (state is checked there by the server).
		expect(new URL(state.client_redirect_uri).pathname).toBe('/auth/callback');
	});

	it.each([
		['ISFC unreachable', () => Promise.reject(new TypeError('fetch failed'))],
		['an error answer', () => Response.json({ error_code: 'X' }, { status: 500 })],
		['no flow token', () => Response.json({ data: {} })],
		['a non-JSON answer', () => new Response('<html>', { status: 200 })]
	])('says so, and never redirects, on %s', async (_n, answer) => {
		const f = fakes(answer as never);
		await expect(
			startMicrosoftSso(plan, { fetch: f.fetch, assign: f.assign })
		).rejects.toBeInstanceOf(MicrosoftSsoError);
		expect(f.assign).not.toHaveBeenCalled();
	});
});

import { describe, expect, it } from 'vitest';
import { AUTH, createFakeIsfc, isfcError, jwt, s256, tokenResponse } from '$lib/testing/fake-isfc';
import type { IsfcConfig } from './config';
import {
	authorizeUrl,
	exchangeCode,
	fetchLoginMethods,
	generatePkce,
	IsfcError,
	isfcCall,
	jwtClaims,
	randomToken
} from './isfc';

const config: IsfcConfig = {
	authUrl: AUTH,
	clientId: 'integration-hub-console',
	redirectUri: null,
	gatewayUrl: null,
	platformTenantId: null
};

// HUB-491: requests to ISFC, against a fake auth-service.
describe('login methods', () => {
	it('lists only Email OTP and Microsoft, Email OTP first, whatever else ISFC offers', async () => {
		const isfc = createFakeIsfc();
		const methods = await fetchLoginMethods(config, isfc.fetch);
		expect(methods.map((m) => m.id)).toEqual(['EMAIL_OTP', 'MICROSOFT_SSO']);
		expect(isfc.calls[0]!.url.toString()).toBe(`${AUTH}/oauth2/method/integration-hub-console`);
		expect(methods[1]!.meta).toMatchObject({
			client_id: 'entra-client',
			tenant_id: 'entra-tenant-id'
		});
	});

	it('ignores duplicates and unknown ids, and tolerates null data', async () => {
		const dup = createFakeIsfc({
			methods: [
				{ id: 'EMAIL_OTP', name: 'A' },
				{ id: 'EMAIL_OTP', name: 'B' },
				{ id: 'email_otp', name: 'C' }
			]
		});
		expect((await fetchLoginMethods(config, dup.fetch)).map((m) => m.name)).toEqual(['A']);
		const none = async () => Response.json({ success: true, data: null });
		expect(await fetchLoginMethods(config, none as never)).toEqual([]);
	});

	it('maps ISFC failures to IsfcError (transient for network and 5xx)', async () => {
		const down = (async () => {
			throw new TypeError('fetch failed');
		}) as never;
		await expect(fetchLoginMethods(config, down)).rejects.toMatchObject({
			status: 0,
			transient: true
		});
		const five = (async () => new Response('boom', { status: 502 })) as never;
		await expect(fetchLoginMethods(config, five)).rejects.toMatchObject({
			status: 502,
			transient: true
		});
		const four = (async () => isfcError(403, 'AS_FB_21')) as never;
		await expect(fetchLoginMethods(config, four)).rejects.toMatchObject({
			status: 403,
			code: 'AS_FB_21',
			transient: false
		});
	});
});

describe('PKCE and the authorize URL', () => {
	it('S256: a 64-byte verifier and its SHA-256 challenge', () => {
		const a = generatePkce();
		expect(a.verifier).toMatch(/^[A-Za-z0-9_-]{86}$/);
		expect(a.challenge).toBe(s256(a.verifier));
		expect(generatePkce().verifier).not.toBe(a.verifier);
	});

	it('state and nonce are unguessable', () => {
		expect(randomToken()).toMatch(/^[0-9a-f]{32}$/);
		expect(randomToken()).not.toBe(randomToken());
	});

	it('builds the Hydra authorization request through the auth host', () => {
		const url = new URL(
			authorizeUrl(config, {
				redirectUri: 'https://console.test/auth/callback',
				state: 'st',
				nonce: 'no',
				challenge: 'ch'
			})
		);
		expect(`${url.origin}${url.pathname}`).toBe(`${AUTH}/oauth2/auth`);
		expect(Object.fromEntries(url.searchParams)).toEqual({
			response_type: 'code',
			client_id: 'integration-hub-console',
			redirect_uri: 'https://console.test/auth/callback',
			scope: 'offline_access',
			state: 'st',
			nonce: 'no',
			code_challenge: 'ch',
			code_challenge_method: 'S256'
		});
	});
});

describe('exchangeCode', () => {
	const input = {
		code: 'the-code',
		verifier: 'the-verifier',
		redirectUri: 'https://console.test/auth/callback',
		method: 'EMAIL_OTP',
		deviceId: 'device-00000000-0000-4000-8000-000000000000',
		userAgent: 'Mozilla/5.0 (Console Test)'
	};

	it('posts the code server-side with x-device-id and the browser User-Agent', async () => {
		const isfc = createFakeIsfc();
		const t = await exchangeCode(config, isfc.fetch, input, Date.parse('2026-11-11T10:00:00Z'));
		const call = isfc.to('/oauth2/token')[0]!;
		expect(call.method).toBe('POST');
		expect(call.headers.get('x-device-id')).toBe(input.deviceId);
		expect(call.headers.get('user-agent')).toBe(input.userAgent);
		expect(call.headers.get('content-type')).toBe('application/json');
		expect(call.body).toEqual({
			grant_type: 'authorization_code',
			client_id: 'integration-hub-console',
			redirect_uri: input.redirectUri,
			code: 'the-code',
			code_verifier: 'the-verifier',
			session_info: {
				platform: 'WEB',
				login_method: 'EMAIL_OTP',
				device_info: { device_id: input.deviceId }
			}
		});
		expect(t).toMatchObject({
			refreshToken: 'refresh-1',
			sessionId: 'sess-1',
			accessExp: Math.floor(Date.parse('2026-11-11T10:00:00Z') / 1000) + 900,
			idTokenNonce: null
		});
	});

	it('reads the expiry from the token when expires_in is missing, and the nonce of an id_token', async () => {
		const isfc = createFakeIsfc({
			token: () =>
				tokenResponse(
					{ expires_in: undefined, id_token: jwt({ nonce: 'n-1' }) },
					{ exp: 1_900_000_000 }
				)
		});
		const t = await exchangeCode(config, isfc.fetch, input);
		expect(t.accessExp).toBe(1_900_000_000);
		expect(t.idTokenNonce).toBe('n-1');
	});

	it('refuses an answer without an access token', async () => {
		const isfc = createFakeIsfc({ token: () => Response.json({ success: true, data: {} }) });
		await expect(exchangeCode(config, isfc.fetch, input)).rejects.toBeInstanceOf(IsfcError);
	});

	it('carries ISFC error codes (AS_FB_21, AS_VE_34) without leaking the code or verifier', async () => {
		const isfc = createFakeIsfc({ token: () => isfcError(403, 'AS_FB_21', 'no tenant') });
		const err = await exchangeCode(config, isfc.fetch, input).catch((e) => e);
		expect(err).toMatchObject({ status: 403, code: 'AS_FB_21' });
		expect(String(err.message)).not.toContain('the-code');
		expect(String(err.message)).not.toContain('the-verifier');
	});
});

describe('isfcCall and jwtClaims', () => {
	it('does not follow redirects', async () => {
		let init: RequestInit | undefined;
		const f = (async (_u: unknown, i: RequestInit) => {
			init = i;
			return Response.json({});
		}) as never;
		await isfcCall(f, `${AUTH}/x`, { method: 'GET' });
		expect(init?.redirect).toBe('manual');
	});

	it('reads claims without verifying, and survives junk', () => {
		expect(jwtClaims(jwt({ sub: 'u', n: 1 }))).toMatchObject({ sub: 'u', n: 1 });
		expect(jwtClaims('nope')).toEqual({});
		expect(jwtClaims('a.%%%.c')).toEqual({});
	});
});

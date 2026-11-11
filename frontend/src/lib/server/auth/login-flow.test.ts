import { describe, expect, it } from 'vitest';
import { AUTH, createFakeIsfc, isfcError, jwt, s256, tokenResponse } from '$lib/testing/fake-isfc';
import type { IsfcConfig } from './config';
import {
	LOGIN_TTL_SECONDS,
	completeLogin,
	isDeviceId,
	newDeviceId,
	openLoginState,
	sealLoginState,
	startLogin,
	type LoginState
} from './login-flow';

const SECRET = 's'.repeat(40);
const ORIGIN = 'https://console.test';
const DEVICE = 'device-00000000-0000-4000-8000-000000000000';
const NOW = Date.parse('2026-11-11T10:00:00Z');
const config: IsfcConfig = {
	authUrl: AUTH,
	clientId: 'integration-hub-console',
	redirectUri: null,
	gatewayUrl: null,
	platformTenantId: null
};

// HUB-491: starting a sign-in (PKCE, state, nonce in a sealed cookie) and finishing it at the callback.
describe('startLogin', () => {
	it('Email OTP: PKCE S256 + state + nonce in the URL, the verifier only in the login state', async () => {
		const isfc = createFakeIsfc();
		const r = await startLogin(
			config,
			isfc.fetch,
			{ method: 'EMAIL_OTP', next: '/tenants', origin: ORIGIN, deviceId: DEVICE },
			NOW
		);
		if (!r.ok || r.kind !== 'redirect') throw new Error('expected a redirect');
		const url = new URL(r.url);
		expect(`${url.origin}${url.pathname}`).toBe(`${AUTH}/oauth2/auth`);
		expect(url.searchParams.get('redirect_uri')).toBe(`${ORIGIN}/auth/callback`);
		expect(url.searchParams.get('code_challenge_method')).toBe('S256');
		expect(url.searchParams.get('code_challenge')).toBe(s256(r.loginState.verifier));
		expect(url.searchParams.get('state')).toBe(r.loginState.state);
		expect(url.searchParams.get('nonce')).toBe(r.loginState.nonce);
		expect(r.url).not.toContain(r.loginState.verifier);
		expect(r.loginState).toMatchObject({
			v: 1,
			method: 'EMAIL_OTP',
			next: '/tenants',
			iat: NOW / 1000
		});
		expect(r.loginState.state).not.toBe(r.loginState.nonce);
	});

	it('uses the configured redirect URI when there is one', async () => {
		const r = await startLogin(
			{ ...config, redirectUri: 'https://hub.example.com/auth/callback' },
			createFakeIsfc().fetch,
			{ method: 'EMAIL_OTP', next: null, origin: ORIGIN, deviceId: DEVICE }
		);
		if (!r.ok || r.kind !== 'redirect') throw new Error('expected a redirect');
		expect(new URL(r.url).searchParams.get('redirect_uri')).toBe(
			'https://hub.example.com/auth/callback'
		);
	});

	it('never keeps an off-site next', async () => {
		const r = await startLogin(config, createFakeIsfc().fetch, {
			method: 'EMAIL_OTP',
			next: '//evil.example',
			origin: ORIGIN,
			deviceId: DEVICE
		});
		expect(r).toMatchObject({ ok: true, loginState: { next: '/' } });
	});

	it('Microsoft: a plan for the browser with the server-issued challenge and the method meta', async () => {
		const r = await startLogin(config, createFakeIsfc().fetch, {
			method: 'MICROSOFT_SSO',
			next: '/',
			origin: ORIGIN,
			deviceId: DEVICE
		});
		if (!r.ok || r.kind !== 'microsoft') throw new Error('expected a plan');
		expect(r.plan).toMatchObject({
			deviceId: DEVICE,
			clientId: 'integration-hub-console',
			consoleRedirectUri: `${ORIGIN}/auth/callback`,
			idp: {
				clientId: 'entra-client',
				redirectUri: `${AUTH}/oauth2/callback`,
				tenantId: 'entra-tenant-id',
				domainHint: 'example.com',
				countryCode: 'IN'
			}
		});
		const flow = new URL(r.plan.flowUrl);
		expect(flow.searchParams.get('code_challenge')).toBe(s256(r.loginState.verifier));
		expect(flow.searchParams.get('state')).toBe(r.loginState.state);
		// The verifier never goes to the browser.
		expect(JSON.stringify(r.plan)).not.toContain(r.loginState.verifier);
	});

	it.each([
		['no client id', { redirect_uri: `${AUTH}/oauth2/callback`, tenant_id: 'abc' }],
		['no tenant id', { client_id: 'c', redirect_uri: `${AUTH}/oauth2/callback` }],
		[
			'a javascript: redirect',
			{ client_id: 'c', redirect_uri: 'javascript:alert(1)', tenant_id: 'abc' }
		],
		[
			'a tenant id with a slash',
			{ client_id: 'c', redirect_uri: `${AUTH}/oauth2/callback`, tenant_id: 'a/b' }
		]
	])('Microsoft with %s in ISFC is refused with a clear message', async (_n, meta) => {
		const isfc = createFakeIsfc({
			methods: [{ id: 'MICROSOFT_SSO', name: 'Microsoft', meta_data: meta }]
		});
		const r = await startLogin(config, isfc.fetch, {
			method: 'MICROSOFT_SSO',
			next: '/',
			origin: ORIGIN,
			deviceId: DEVICE
		});
		expect(r).toMatchObject({
			ok: false,
			status: 503,
			message: expect.stringMatching(/not fully set up/)
		});
	});

	it.each(['GOOGLE_SSO', 'PHONE_OTP', '', 'email_otp'])('refuses the method %j', async (method) => {
		const r = await startLogin(config, createFakeIsfc().fetch, {
			method,
			next: '/',
			origin: ORIGIN,
			deviceId: DEVICE
		});
		expect(r).toMatchObject({ ok: false, status: 400 });
	});

	it('refuses a method ISFC does not have active for the console', async () => {
		const isfc = createFakeIsfc({ methods: [{ id: 'MICROSOFT_SSO', name: 'Microsoft' }] });
		const r = await startLogin(config, isfc.fetch, {
			method: 'EMAIL_OTP',
			next: '/',
			origin: ORIGIN,
			deviceId: DEVICE
		});
		expect(r).toMatchObject({
			ok: false,
			status: 400,
			message: 'This sign-in method is not available.'
		});
	});

	it('says so when ISFC does not answer', async () => {
		const down = (async () => {
			throw new TypeError('fetch failed');
		}) as never;
		const r = await startLogin(config, down, {
			method: 'EMAIL_OTP',
			next: '/',
			origin: ORIGIN,
			deviceId: DEVICE
		});
		expect(r).toMatchObject({ ok: false, status: 503 });
	});
});

describe('login state cookie', () => {
	const state: LoginState = {
		v: 1,
		state: 's',
		nonce: 'n',
		verifier: 'secret-verifier-value',
		method: 'EMAIL_OTP',
		next: '/',
		iat: NOW / 1000
	};

	it('is sealed: the verifier is not readable in it', () => {
		const v = sealLoginState(state, SECRET);
		expect(v).not.toContain('secret-verifier-value');
		expect(Buffer.from(v, 'base64url').toString('latin1')).not.toContain('secret-verifier-value');
		expect(openLoginState(v, SECRET, NOW)).toEqual(state);
	});

	it('is short-lived: valid for 10 minutes, then refused', () => {
		const v = sealLoginState(state, SECRET);
		expect(LOGIN_TTL_SECONDS).toBe(600);
		expect(openLoginState(v, SECRET, NOW + 599_000)).not.toBeNull();
		expect(openLoginState(v, SECRET, NOW + 601_000)).toBeNull();
	});

	it('is refused with another secret, tampering, or as nothing', () => {
		const v = sealLoginState(state, SECRET);
		expect(openLoginState(v, 'x'.repeat(40), NOW)).toBeNull();
		expect(openLoginState(v.slice(0, -4) + 'AAAA', SECRET, NOW)).toBeNull();
		expect(openLoginState(undefined, SECRET, NOW)).toBeNull();
	});

	it('device ids look like device-<uuid>', () => {
		expect(isDeviceId(newDeviceId())).toBe(true);
		expect(isDeviceId('device-1')).toBe(false);
		expect(isDeviceId(undefined)).toBe(false);
	});
});

describe('completeLogin', () => {
	const login: LoginState = {
		v: 1,
		state: 'expected-state',
		nonce: 'expected-nonce',
		verifier: 'the-verifier',
		method: 'EMAIL_OTP',
		next: '/dlq',
		iat: NOW / 1000
	};
	const base = {
		code: 'abc',
		state: 'expected-state',
		error: null,
		errorCode: null,
		loginCookie: sealLoginState(login, SECRET),
		origin: ORIGIN,
		deviceId: DEVICE,
		userAgent: 'UA/1',
		secret: SECRET
	};

	it('validates state, exchanges the code and builds the session', async () => {
		const isfc = createFakeIsfc();
		const r = await completeLogin(config, isfc.fetch, base, NOW);
		if (!r.ok) throw new Error(`failed: ${r.code}`);
		expect(r.next).toBe('/dlq');
		expect(r.session).toMatchObject({
			v: 2,
			provider: 'isfc',
			user: {
				subject: 'user-1',
				name: 'Priya Sharma',
				email: 'priya@example.com',
				roles: [],
				tenants: []
			},
			refreshToken: 'refresh-1',
			isfcSessionId: 'sess-1',
			deviceId: DEVICE,
			tenantId: null
		});
		expect(r.session.accessToken).toContain('.');
		const call = isfc.to('/oauth2/token')[0]!;
		expect(call.body).toMatchObject({
			code: 'abc',
			code_verifier: 'the-verifier',
			redirect_uri: `${ORIGIN}/auth/callback`
		});
		expect(call.headers.get('x-device-id')).toBe(DEVICE);
		expect(call.headers.get('user-agent')).toBe('UA/1');
	});

	it('refuses a wrong or missing state without calling ISFC', async () => {
		for (const state of ['other', null, '', 'expected-state-extra']) {
			const isfc = createFakeIsfc();
			const r = await completeLogin(config, isfc.fetch, { ...base, state }, NOW);
			expect(r).toEqual({ ok: false, code: 'state_mismatch' });
			expect(isfc.calls).toHaveLength(0);
		}
	});

	it('refuses when there is no login cookie, or it is older than 10 minutes', async () => {
		const isfc = createFakeIsfc();
		expect(
			await completeLogin(config, isfc.fetch, { ...base, loginCookie: undefined }, NOW)
		).toEqual({
			ok: false,
			code: 'login_expired'
		});
		expect(await completeLogin(config, isfc.fetch, base, NOW + 11 * 60_000)).toEqual({
			ok: false,
			code: 'login_expired'
		});
		expect(isfc.calls).toHaveLength(0);
	});

	it('a login cookie sealed with another secret is as good as none', async () => {
		const r = await completeLogin(
			config,
			createFakeIsfc().fetch,
			{ ...base, secret: 'q'.repeat(40) },
			NOW
		);
		expect(r).toEqual({ ok: false, code: 'login_expired' });
	});

	it('an error from the provider ends the attempt; only known codes are passed on', async () => {
		const isfc = createFakeIsfc();
		expect(
			await completeLogin(config, isfc.fetch, { ...base, error: 'access_denied', code: null }, NOW)
		).toEqual({
			ok: false,
			code: 'access_denied'
		});
		expect(
			await completeLogin(config, isfc.fetch, { ...base, errorCode: 'AS_VE_34' }, NOW)
		).toEqual({
			ok: false,
			code: 'AS_VE_34'
		});
		expect(
			await completeLogin(config, isfc.fetch, { ...base, errorCode: '<script>' }, NOW)
		).toEqual({
			ok: false,
			code: 'access_denied'
		});
		expect(isfc.calls).toHaveLength(0);
	});

	it('needs a code', async () => {
		expect(
			await completeLogin(config, createFakeIsfc().fetch, { ...base, code: null }, NOW)
		).toEqual({
			ok: false,
			code: 'sign_in_failed'
		});
	});

	it.each([
		['AS_FB_21', 403],
		['AS_VE_34', 400]
	])(
		'the ISFC error %s from the exchange reaches the login page as that code',
		async (code, status) => {
			const isfc = createFakeIsfc({ token: () => isfcError(status, code) });
			expect(await completeLogin(config, isfc.fetch, base, NOW)).toEqual({ ok: false, code });
		}
	);

	it('an unknown ISFC error becomes a generic failure, ISFC down becomes unavailable', async () => {
		expect(
			await completeLogin(
				config,
				createFakeIsfc({ token: () => isfcError(400, 'XX_99') }).fetch,
				base,
				NOW
			)
		).toEqual({ ok: false, code: 'sign_in_failed' });
		expect(
			await completeLogin(
				config,
				createFakeIsfc({ token: () => new Response('x', { status: 503 }) }).fetch,
				base,
				NOW
			)
		).toEqual({ ok: false, code: 'unavailable' });
	});

	it('an id_token with another nonce is refused; one with ours is accepted', async () => {
		const bad = createFakeIsfc({
			token: () => tokenResponse({ id_token: jwt({ nonce: 'someone-elses' }) })
		});
		expect(await completeLogin(config, bad.fetch, base, NOW)).toEqual({
			ok: false,
			code: 'state_mismatch'
		});
		const good = createFakeIsfc({
			token: () => tokenResponse({ id_token: jwt({ nonce: 'expected-nonce' }) })
		});
		expect(await completeLogin(config, good.fetch, base, NOW)).toMatchObject({ ok: true });
	});

	it('refuses a token without a subject', async () => {
		const isfc = createFakeIsfc({
			token: () => tokenResponse({ access_token: jwt({ email: 'x@y.z' }) })
		});
		expect(await completeLogin(config, isfc.fetch, base, NOW)).toEqual({
			ok: false,
			code: 'sign_in_failed'
		});
	});
});

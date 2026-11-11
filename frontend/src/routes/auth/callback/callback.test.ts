import { isRedirect } from '@sveltejs/kit';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AUTH, createFakeIsfc, isfcError } from '$lib/testing/fake-isfc';
import { decodeSession } from '$lib/server/auth/session';
import { readSessionCookie } from '$lib/server/auth/cookie';
import { LOGIN_COOKIE, sealLoginState, type LoginState } from '$lib/server/auth/login-flow';
import { GET } from './+server';

// HUB-491: /auth/callback validates state, exchanges the code on the server, sets the session.
const SECRET = 's'.repeat(40);
const env = vi.hoisted(() => ({
	priv: {} as Record<string, string | undefined>,
	pub: {} as Record<string, string | undefined>
}));
vi.mock('$env/dynamic/private', () => ({ env: env.priv }));
vi.mock('$env/dynamic/public', () => ({ env: env.pub }));

const DEVICE = 'device-00000000-0000-4000-8000-000000000009';
const login: LoginState = {
	v: 1,
	state: 'st-1',
	nonce: 'no-1',
	verifier: 'verifier-1',
	method: 'EMAIL_OTP',
	next: '/tenants',
	iat: Math.floor(Date.now() / 1000)
};

beforeEach(() => {
	for (const k of Object.keys(env.priv)) delete env.priv[k];
	Object.assign(env.priv, { HUB_ISFC_AUTH_URL: AUTH, HUB_SESSION_SECRET: SECRET });
	env.pub.PUBLIC_AUTH_MODE = 'isfc';
});

function run(
	search: string,
	opts: { cookies?: Record<string, string>; fetch?: typeof fetch; ua?: string } = {}
) {
	const store = new Map<string, string>(
		Object.entries({
			hub_device: DEVICE,
			[LOGIN_COOKIE]: sealLoginState(login, SECRET),
			...opts.cookies
		})
	);
	const deleted: { name: string; opts: Record<string, unknown> }[] = [];
	const sets: { name: string; value: string; opts: Record<string, unknown> }[] = [];
	const cookies = {
		get: (k: string) => store.get(k),
		set: (name: string, value: string, o: Record<string, unknown>) => {
			store.set(name, value);
			sets.push({ name, value, opts: o });
		},
		delete: (name: string, o: Record<string, unknown>) => {
			store.delete(name);
			deleted.push({ name, opts: o });
		}
	};
	const isfc = createFakeIsfc();
	const result = GET({
		url: new URL(`https://console.test/auth/callback${search}`),
		cookies,
		request: new Request('https://console.test/auth/callback', {
			headers: { 'user-agent': opts.ua ?? 'Browser/1.0' }
		}),
		fetch: opts.fetch ?? isfc.fetch
	} as never);
	return { result, cookies, store, deleted, sets, isfc };
}

async function redirectOf(p: unknown) {
	try {
		await p;
	} catch (e) {
		if (isRedirect(e)) return e;
		throw e;
	}
	throw new Error('expected a redirect');
}

describe('GET /auth/callback', () => {
	it('exchanges the code, sets the encrypted session, clears the login cookie and drops the code from the URL', async () => {
		const r = run('?code=the-code&state=st-1');
		const to = await redirectOf(r.result);
		expect(to).toMatchObject({ status: 303, location: '/tenants' });
		// The code never stays in the address bar.
		expect(to.location).not.toContain('code');
		const call = r.isfc.to('/oauth2/token')[0]!;
		expect(call.body).toMatchObject({ code: 'the-code', code_verifier: 'verifier-1' });
		expect(call.headers.get('x-device-id')).toBe(DEVICE);
		expect(call.headers.get('user-agent')).toBe('Browser/1.0');
		expect(r.deleted).toContainEqual({ name: LOGIN_COOKIE, opts: { path: '/auth', secure: true } });
		const cookie = r.sets.find((s) => s.name === 'hub_console_session')!;
		expect(cookie.opts).toMatchObject({ path: '/', httpOnly: true, sameSite: 'lax', secure: true });
		expect(cookie.value).not.toContain('refresh-1');
		const s = decodeSession(readSessionCookie(r.cookies), SECRET)!;
		expect(s).toMatchObject({ provider: 'isfc', refreshToken: 'refresh-1', deviceId: DEVICE });
	});

	it('a wrong state is refused, ISFC is never called, and no session is set', async () => {
		const r = run('?code=the-code&state=forged');
		expect(await redirectOf(r.result)).toMatchObject({ location: '/login?error=state_mismatch' });
		expect(r.isfc.calls).toHaveLength(0);
		expect(r.sets.some((s) => s.name === 'hub_console_session')).toBe(false);
		expect(r.deleted.some((d) => d.name === LOGIN_COOKIE)).toBe(true);
	});

	it('is single use: the login cookie is gone after the first try, success or not', async () => {
		const r = run('?code=c&state=st-1');
		await redirectOf(r.result);
		expect(r.store.has(LOGIN_COOKIE)).toBe(false);
	});

	it('without a login cookie (another browser, or expired) the sign-in must be started again', async () => {
		const store = run('?code=c&state=st-1', { cookies: { [LOGIN_COOKIE]: '' } });
		expect(await redirectOf(store.result)).toMatchObject({
			location: '/login?error=login_expired'
		});
	});

	it.each([
		['AS_FB_21', 403],
		['AS_VE_34', 400]
	])('%s from ISFC lands on the login page with that code', async (code, status) => {
		const isfc = createFakeIsfc({ token: () => isfcError(status, code) });
		const r = run('?code=c&state=st-1', { fetch: isfc.fetch });
		expect(await redirectOf(r.result)).toMatchObject({ location: `/login?error=${code}` });
		expect(r.sets.some((s) => s.name === 'hub_console_session')).toBe(false);
	});

	it('an error from the identity provider comes back as a message, not as text from the URL', async () => {
		const r = run('?error=access_denied&error_description=%3Cb%3Ex%3C/b%3E&state=st-1');
		const to = await redirectOf(r.result);
		expect(to.location).toBe('/login?error=access_denied');
		expect(to.location).not.toContain('error_description');
	});

	it('goes to the page the operator asked for, never off-site', async () => {
		const evil = sealLoginState({ ...login, next: '//evil.example' }, SECRET);
		const r = run('?code=c&state=st-1', { cookies: { [LOGIN_COOKIE]: evil } });
		// `startLogin` already passed `next` through safeNext; it is checked again here.
		expect(await redirectOf(r.result)).toMatchObject({ location: '/' });
	});

	it('is a 404 outside ISFC mode', async () => {
		env.pub.PUBLIC_AUTH_MODE = 'dev';
		await expect(run('?code=c&state=st-1').result).rejects.toMatchObject({ status: 404 });
	});

	it('without the session secret it cannot sign anyone in', async () => {
		delete env.priv.HUB_SESSION_SECRET;
		expect(await redirectOf(run('?code=c&state=st-1').result)).toMatchObject({
			location: '/login?error=sign_in_failed'
		});
	});
});

import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AUTH, createFakeIsfc, s256 } from '$lib/testing/fake-isfc';
import { LOGIN_COOKIE, openLoginState } from '$lib/server/auth/login-flow';
import Page from './+page.svelte';
import { actions, load } from './+page.server';

// HUB-491 / HUB-493: the login page with ISFC, against a fake auth-service.
const SECRET = 's'.repeat(40);
const env = vi.hoisted(() => ({
	priv: {} as Record<string, string | undefined>,
	pub: {} as Record<string, string | undefined>
}));
vi.mock('$env/dynamic/private', () => ({ env: env.priv }));
vi.mock('$env/dynamic/public', () => ({ env: env.pub }));

beforeEach(() => {
	for (const k of Object.keys(env.priv)) delete env.priv[k];
	Object.assign(env.priv, { HUB_ISFC_AUTH_URL: AUTH, HUB_SESSION_SECRET: SECRET });
	env.pub.PUBLIC_AUTH_MODE = 'isfc';
});

function jar(initial: Record<string, string> = {}) {
	const store = new Map(Object.entries(initial));
	const sets: { name: string; value: string; opts: Record<string, unknown> }[] = [];
	return {
		store,
		sets,
		get: (k: string) => store.get(k),
		set: (name: string, value: string, opts: Record<string, unknown>) => {
			store.set(name, value);
			sets.push({ name, value, opts });
		},
		delete: (k: string) => void store.delete(k)
	};
}

function startEvent(
	fields: Record<string, string>,
	over: { fetch?: typeof fetch; enhanced?: boolean; cookies?: ReturnType<typeof jar> } = {}
) {
	const body = new FormData();
	for (const [k, v] of Object.entries(fields)) body.set(k, v);
	const cookies = over.cookies ?? jar();
	return {
		cookies,
		event: {
			request: new Request('https://console.test/login?/start', {
				method: 'POST',
				body,
				headers: over.enhanced ? { 'x-sveltekit-action': 'true' } : undefined
			}),
			cookies,
			url: new URL('https://console.test/login'),
			fetch: over.fetch ?? createFakeIsfc().fetch
		}
	};
}

describe('login page load', () => {
	const run = (search = '', fetchImpl = createFakeIsfc().fetch) => {
		vi.stubGlobal('fetch', fetchImpl);
		return load({
			url: new URL(`https://console.test/login${search}`),
			locals: { session: null, sessionEnded: false }
		} as never);
	};

	it('offers Email OTP and Microsoft from ISFC, and nothing else', async () => {
		const out = await run();
		expect(out).toMatchObject({
			login: {
				provider: 'isfc',
				ready: true,
				methods: [
					{ id: 'EMAIL_OTP', label: 'Email OTP' },
					{ id: 'MICROSOFT_SSO', label: 'Microsoft' }
				]
			}
		});
	});

	it.each([
		['AS_FB_21', /no tenant access/],
		['AS_VE_34', /email domain is not allowed/],
		['GW_007', /session has ended/],
		['GW_015', /no access to that tenant/]
	])('shows the message for %s', async (code, text) => {
		const out = (await run(`?error=${code}`)) as { error: { code: string; message: string } };
		expect(out.error.code).toBe(code);
		expect(out.error.message).toMatch(text);
	});

	it('echoes nothing it does not know', async () => {
		const out = (await run('?error=<script>alert(1)</script>')) as { error: unknown };
		expect(out.error).toBeNull();
	});

	it('says sign-in is unavailable when ISFC does not answer, or the setting is missing', async () => {
		const down = (async () => {
			throw new TypeError('fetch failed');
		}) as never;
		expect(await run('', down)).toMatchObject({ login: { ready: false } });
		delete env.priv.HUB_ISFC_AUTH_URL;
		expect(await run()).toMatchObject({ login: { ready: false, missing: ['HUB_ISFC_AUTH_URL'] } });
	});

	it('flags a session that ended', async () => {
		vi.stubGlobal('fetch', createFakeIsfc().fetch);
		const out = await load({
			url: new URL('https://console.test/login'),
			locals: { session: null, sessionEnded: true }
		} as never);
		expect(out).toMatchObject({ expired: true });
	});
});

describe('start action', () => {
	it('Email OTP: seals PKCE, state and nonce in a short-lived httpOnly cookie and redirects to Hydra', async () => {
		const { event, cookies } = startEvent({ method: 'EMAIL_OTP', next: '/dlq' });
		const err = (await Promise.resolve(actions.start!(event as never)).catch((e) => e)) as {
			status: number;
			location: string;
		};
		expect(err).toMatchObject({ status: 303 });
		const to = new URL(err.location);
		expect(`${to.origin}${to.pathname}`).toBe(`${AUTH}/oauth2/auth`);
		const login = cookies.sets.find((s) => s.name === LOGIN_COOKIE)!;
		expect(login.opts).toMatchObject({
			path: '/auth',
			httpOnly: true,
			sameSite: 'lax',
			secure: true,
			maxAge: 600
		});
		const state = openLoginState(login.value, SECRET)!;
		expect(state).toMatchObject({ method: 'EMAIL_OTP', next: '/dlq' });
		expect(to.searchParams.get('state')).toBe(state.state);
		expect(to.searchParams.get('nonce')).toBe(state.nonce);
		expect(to.searchParams.get('code_challenge')).toBe(s256(state.verifier));
		expect(login.value).not.toContain(state.verifier);
		// The browser gets a device id for ISFC.
		expect(cookies.sets.some((s) => s.name === 'hub_device' && s.opts.httpOnly === true)).toBe(
			true
		);
	});

	it('Microsoft (with JavaScript): answers the plan and sets the login cookie', async () => {
		const { event, cookies } = startEvent(
			{ method: 'MICROSOFT_SSO', next: '/' },
			{ enhanced: true }
		);
		const out = (await actions.start!(event as never)) as {
			plan: { flowUrl: string; deviceId: string };
		};
		expect(out.plan.flowUrl).toContain(`${AUTH}/oauth2/auth?`);
		expect(out.plan.deviceId).toBe(cookies.store.get('hub_device'));
		const state = openLoginState(cookies.store.get(LOGIN_COOKIE), SECRET)!;
		expect(state.method).toBe('MICROSOFT_SSO');
		expect(JSON.stringify(out)).not.toContain(state.verifier);
	});

	it('Microsoft without JavaScript says so and sets no cookie', async () => {
		const { event, cookies } = startEvent({ method: 'MICROSOFT_SSO' });
		const out = await actions.start!(event as never);
		expect(out).toMatchObject({
			status: 400,
			data: { message: expect.stringMatching(/needs JavaScript/) }
		});
		expect(cookies.store.has(LOGIN_COOKIE)).toBe(false);
	});

	it.each([
		['an unknown method', { method: 'GOOGLE_SSO' }, 400],
		['no method', {}, 400]
	])('refuses %s', async (_n, fields, status) => {
		const { event, cookies } = startEvent(fields);
		expect(await actions.start!(event as never)).toMatchObject({ status });
		expect(cookies.store.has(LOGIN_COOKIE)).toBe(false);
	});

	it('refuses when the secret or the ISFC setting is missing, or the mode is not isfc', async () => {
		delete env.priv.HUB_SESSION_SECRET;
		expect(await actions.start!(startEvent({ method: 'EMAIL_OTP' }).event as never)).toMatchObject({
			status: 503
		});
		env.priv.HUB_SESSION_SECRET = SECRET;
		delete env.priv.HUB_ISFC_AUTH_URL;
		expect(await actions.start!(startEvent({ method: 'EMAIL_OTP' }).event as never)).toMatchObject({
			status: 503
		});
		env.priv.HUB_ISFC_AUTH_URL = AUTH;
		env.pub.PUBLIC_AUTH_MODE = 'dev';
		expect(await actions.start!(startEvent({ method: 'EMAIL_OTP' }).event as never)).toMatchObject({
			status: 503
		});
	});

	it('keeps the same device id across sign-ins', async () => {
		const cookies = jar({ hub_device: 'device-00000000-0000-4000-8000-000000000001' });
		const { event } = startEvent({ method: 'MICROSOFT_SSO' }, { enhanced: true, cookies });
		const out = (await actions.start!(event as never)) as { plan: { deviceId: string } };
		expect(out.plan.deviceId).toBe('device-00000000-0000-4000-8000-000000000001');
		expect(cookies.sets.some((s) => s.name === 'hub_device')).toBe(false);
	});
});

describe('login page', () => {
	const methods = [
		{ id: 'EMAIL_OTP', label: 'Email OTP' },
		{ id: 'MICROSOFT_SSO', label: 'Microsoft' }
	];
	const data = (over: Record<string, unknown> = {}) => ({
		login: { provider: 'isfc', ready: true, notice: null, missing: [], methods },
		next: '/',
		expired: false,
		error: null,
		signedInAs: null,
		...over
	});

	it('shows exactly the two ISFC buttons, no role picker', () => {
		render(Page, { props: { data: data() as never, form: null } });
		expect(screen.getByRole('button', { name: 'Sign in with Email OTP' })).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Sign in with Microsoft' })).toBeInTheDocument();
		expect(screen.getAllByRole('button')).toHaveLength(2);
		expect(screen.queryByRole('radio')).toBeNull();
	});

	it('the Email OTP button is a plain POST to ?/start (works without JavaScript)', () => {
		render(Page, { props: { data: data({ next: '/dlq' }) as never, form: null } });
		const form = screen.getByRole('button', { name: 'Sign in with Email OTP' }).closest('form')!;
		expect(form).toHaveAttribute('method', 'POST');
		expect(form).toHaveAttribute('action', '?/start');
		expect(form.querySelector('input[name=method]')).toHaveValue('EMAIL_OTP');
		expect(form.querySelector('input[name=next]')).toHaveValue('/dlq');
	});

	it('names the ISFC error in an alert', () => {
		render(Page, {
			props: {
				data: data({
					error: { code: 'AS_FB_21', message: 'You have no tenant access in the console.' }
				}) as never,
				form: null
			}
		});
		expect(screen.getByRole('alert')).toHaveTextContent('no tenant access');
	});

	it('says the session ended', () => {
		render(Page, { props: { data: data({ expired: true }) as never, form: null } });
		expect(screen.getByRole('status')).toHaveTextContent('Sign in again');
	});

	it('shows only the methods ISFC has active', () => {
		render(Page, {
			props: {
				data: data({
					login: { provider: 'isfc', ready: true, notice: null, missing: [], methods: [methods[0]] }
				}) as never,
				form: null
			}
		});
		expect(screen.queryByRole('button', { name: /Microsoft/ })).toBeNull();
		expect(screen.getByRole('button', { name: /Email OTP/ })).toBeInTheDocument();
	});

	it('is unavailable, with the missing settings, when ISFC is not configured', () => {
		render(Page, {
			props: {
				data: data({
					login: {
						provider: 'isfc',
						ready: false,
						notice: 'not configured',
						missing: ['HUB_ISFC_AUTH_URL'],
						methods: []
					}
				}) as never,
				form: null
			}
		});
		expect(screen.queryByRole('button')).toBeNull();
		expect(screen.getByText('HUB_ISFC_AUTH_URL')).toBeInTheDocument();
	});

	it('the Microsoft button asks the server for the plan, then opens the flow in the browser (HUB-493)', async () => {
		const isfc = createFakeIsfc();
		const assign = vi.fn();
		vi.stubGlobal('location', { ...window.location, assign });
		vi.stubGlobal('fetch', isfc.fetch);
		// The fake `enhance` runs the real `start` action with these cookies and this fetch.
		const { useActions } = await import('$lib/testing/forms');
		useActions(actions as never, { cookies: {} });
		render(Page, { props: { data: data() as never, form: null } });
		await userEvent.click(screen.getByRole('button', { name: 'Sign in with Microsoft' }));
		await vi.waitFor(() => expect(assign).toHaveBeenCalledOnce());
		const entra = new URL(assign.mock.calls[0]![0] as string);
		expect(entra.origin).toBe('https://login.microsoftonline.com');
		expect(entra.pathname).toBe('/entra-tenant-id/oauth2/v2.0/authorize');
		// The flow token came from the fake Hydra flow, fetched by the browser code with credentials.
		const flow = isfc.calls.find((c) => c.url.pathname === '/oauth2/auth')!;
		expect(flow.headers.get('x-device-id')).toMatch(/^device-/);
		void within;
	});
});

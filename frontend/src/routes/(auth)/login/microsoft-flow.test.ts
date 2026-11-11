import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { isRedirect } from '@sveltejs/kit';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { decodeSession } from '$lib/server/auth/session';
import { readSessionCookie } from '$lib/server/auth/cookie';
import { startMicrosoftSso, type MicrosoftPlan } from '$lib/auth/microsoft';
import { useActions } from '$lib/testing/forms';
import { AUTH, createFakeIsfc, s256 } from '$lib/testing/fake-isfc';
import { GET as callback } from '../../auth/callback/+server';
import Page from './+page.svelte';
import { actions } from './+page.server';

// HUB-493: the whole Microsoft round trip against fakes (no real ISFC, Hydra or Entra): the
// server's start action, the browser code, a fake Entra / auth-service / Hydra chain, and the
// server's callback. PKCE and state must bind end to end.
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

function jar() {
	const store = new Map<string, string>();
	return {
		store,
		get: (k: string) => store.get(k),
		set: (k: string, v: string) => void store.set(k, v),
		delete: (k: string) => void store.delete(k)
	};
}

const decodeState = (s: string) => JSON.parse(Buffer.from(s, 'base64url').toString('utf8'));

/** Fake Hydra: remembers what each flow was started with, hands out flow tokens, finishes flows. */
function fakeHydra(isfc: ReturnType<typeof createFakeIsfc>) {
	const flows = new Map<
		string,
		{ state: string; challenge: string; redirectUri: string; credentials: string | undefined }
	>();
	const browserFetch = (async (url: RequestInfo | URL, init?: RequestInit) => {
		const u = new URL(String(url));
		const flow = `flow-${flows.size + 1}`;
		flows.set(flow, {
			state: u.searchParams.get('state')!,
			challenge: u.searchParams.get('code_challenge')!,
			redirectUri: u.searchParams.get('redirect_uri')!,
			credentials: init?.credentials
		});
		// Also let the fake auth-service see it (it records calls).
		await isfc.fetch(String(url), { headers: (init?.headers ?? {}) as HeadersInit });
		return Response.json({ success: true, data: { flow_token: flow, methods: [] } });
	}) as typeof fetch;
	/** Entra -> auth-service callback -> Hydra: where the browser ends up. */
	const finish = (entraUrl: string, code = 'hydra-code-1') => {
		const entra = new URL(entraUrl);
		const blob = decodeState(entra.searchParams.get('state')!);
		const flow = flows.get(blob.flow_token)!;
		const back = new URL(flow.redirectUri);
		back.searchParams.set('code', code);
		back.searchParams.set('state', flow.state);
		return { back, flow, blob };
	};
	return { flows, browserFetch, finish };
}

async function startPlan(cookies: ReturnType<typeof jar>, isfc: ReturnType<typeof createFakeIsfc>) {
	const body = new FormData();
	body.set('method', 'MICROSOFT_SSO');
	body.set('next', '/tenants');
	const out = (await actions.start!({
		request: new Request('https://console.test/login?/start', {
			method: 'POST',
			body,
			headers: { 'x-sveltekit-action': 'true' }
		}),
		cookies,
		url: new URL('https://console.test/login'),
		fetch: isfc.fetch
	} as never)) as { plan: MicrosoftPlan };
	return out.plan;
}

async function callbackWith(
	back: URL,
	cookies: ReturnType<typeof jar>,
	isfc: ReturnType<typeof createFakeIsfc>
) {
	try {
		await callback({
			url: new URL(`https://console.test/auth/callback${back.search}`),
			cookies,
			request: new Request('https://console.test/auth/callback', {
				headers: { 'user-agent': 'Browser/9' }
			}),
			fetch: isfc.fetch
		} as never);
	} catch (e) {
		if (isRedirect(e)) return e;
		throw e;
	}
	throw new Error('expected a redirect');
}

describe('Microsoft round trip against fakes', () => {
	it('start -> browser opens the flow with credentials -> Entra -> callback signs in; PKCE binds', async () => {
		const isfc = createFakeIsfc();
		const hydra = fakeHydra(isfc);
		const cookies = jar();
		const plan = await startPlan(cookies, isfc);

		const assign = vi.fn();
		await startMicrosoftSso(plan, { fetch: hydra.browserFetch, assign });
		const { back, flow, blob } = hydra.finish(assign.mock.calls[0]![0] as string);
		// The browser asked Hydra with cookies included, and ISFC got the device id.
		expect(flow.credentials).toBe('include');
		expect(isfc.to('/oauth2/auth')[0]!.headers.get('x-device-id')).toBe(plan.deviceId);
		// The Entra state names the console and the device.
		expect(blob).toMatchObject({ method_id: 'MICROSOFT_SSO', device_id: plan.deviceId });
		expect(back.pathname).toBe('/auth/callback');

		const to = await callbackWith(back, cookies, isfc);
		expect(to).toMatchObject({ status: 303, location: '/tenants' });
		const exchange = isfc.to('/oauth2/token')[0]!;
		// The verifier that goes to ISFC matches the challenge the browser sent to Hydra.
		expect(s256((exchange.body as { code_verifier: string }).code_verifier)).toBe(flow.challenge);
		expect(exchange.body).toMatchObject({
			code: 'hydra-code-1',
			session_info: { login_method: 'MICROSOFT_SSO' }
		});
		expect(exchange.headers.get('x-device-id')).toBe(plan.deviceId);
		expect(exchange.headers.get('user-agent')).toBe('Browser/9');
		const session = decodeSession(readSessionCookie(cookies), SECRET)!;
		expect(session.provider).toBe('isfc');
		// The code is spent: the login cookie is gone, a replay has nothing to check against.
		expect(cookies.store.has('hub_login')).toBe(false);
		const replay = await callbackWith(back, cookies, isfc);
		expect(replay.location).toBe('/login?error=login_expired');
	});

	it('a callback that lands in another browser (no login cookie), or with a forged state, is refused', async () => {
		const isfc = createFakeIsfc();
		const hydra = fakeHydra(isfc);
		const cookies = jar();
		const plan = await startPlan(cookies, isfc);
		const assign = vi.fn();
		await startMicrosoftSso(plan, { fetch: hydra.browserFetch, assign });
		const { back } = hydra.finish(assign.mock.calls[0]![0] as string);

		const forged = new URL(back);
		forged.searchParams.set('state', 'attacker-state');
		const to = await callbackWith(forged, cookies, isfc);
		expect(to.location).toBe('/login?error=state_mismatch');
		expect(isfc.to('/oauth2/token')).toHaveLength(0);
		expect(decodeSession(readSessionCookie(cookies), SECRET)).toBeNull();

		const other = await callbackWith(back, jar(), isfc);
		expect(other.location).toBe('/login?error=login_expired');
	});
});

describe('Microsoft button', () => {
	const data = {
		login: {
			provider: 'isfc',
			ready: true,
			notice: null,
			missing: [],
			methods: [{ id: 'MICROSOFT_SSO', label: 'Microsoft' }]
		},
		next: '/',
		expired: false,
		error: null,
		signedInAs: null
	};

	it('shows a failure from ISFC and becomes usable again', async () => {
		const isfc = createFakeIsfc({ methods: [{ id: 'EMAIL_OTP', name: 'Email OTP' }] });
		vi.stubGlobal('fetch', isfc.fetch);
		useActions(actions as never, { cookies: {} });
		render(Page, { props: { data: data as never, form: null } });
		const button = screen.getByRole('button', { name: 'Sign in with Microsoft' });
		await userEvent.click(button);
		expect(await screen.findByRole('alert')).toHaveTextContent(
			'This sign-in method is not available.'
		);
		expect(button).toBeEnabled();
	});

	it('says so when the Hydra flow cannot be opened, and does not leave the page', async () => {
		const isfc = createFakeIsfc();
		const assign = vi.fn();
		vi.stubGlobal('location', { ...window.location, assign });
		// The server half works; the browser's own call to ISFC fails (CORS, offline).
		const serverFetch = isfc.fetch;
		vi.stubGlobal('fetch', async (url: RequestInfo | URL, init?: RequestInit) => {
			if (String(url).includes('/oauth2/auth')) throw new TypeError('Failed to fetch');
			return serverFetch(url, init);
		});
		useActions(actions as never, { cookies: {} });
		render(Page, { props: { data: data as never, form: null } });
		const button = screen.getByRole('button', { name: 'Sign in with Microsoft' });
		await userEvent.click(button);
		expect(await screen.findByRole('alert')).toHaveTextContent('ISFC did not answer');
		expect(assign).not.toHaveBeenCalled();
		await waitFor(() => expect(button).toBeEnabled());
	});

	it('is usable again when the page comes back from Entra (back button, bfcache)', async () => {
		const isfc = createFakeIsfc();
		vi.stubGlobal('fetch', isfc.fetch);
		vi.stubGlobal('location', { ...window.location, assign: vi.fn() });
		useActions(actions as never, { cookies: {} });
		render(Page, { props: { data: data as never, form: null } });
		const button = screen.getByRole('button', { name: /Sign in with Microsoft/ });
		await userEvent.click(button);
		await waitFor(() =>
			expect(screen.getByRole('button', { name: /Opening Microsoft/ })).toBeDisabled()
		);
		const ev = new Event('pageshow') as Event & { persisted: boolean };
		ev.persisted = true;
		window.dispatchEvent(ev);
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'Sign in with Microsoft' })).toBeEnabled()
		);
	});
});

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { assertAuthModeAtStartup, authMode, authProvider, sessionSecret } from '$lib/server/auth';
import { encodeSession, newSession } from '$lib/server/auth/session';
import { authenticate } from '$lib/server/auth/hooks';
import { actions, load } from './+page.server';

// HUB-495: PUBLIC_AUTH_MODE=dev|isfc. The dev role picker is refused unless the build is a mock
// build (__API_MOCKING__) or NODE_ENV=test. Vitest runs with NODE_ENV=test, so a deployment is
// simulated by stubbing NODE_ENV.
const env = vi.hoisted(() => ({
	priv: {} as Record<string, string | undefined>,
	pub: {} as Record<string, string | undefined>
}));
vi.mock('$env/dynamic/private', () => ({ env: env.priv }));
vi.mock('$env/dynamic/public', () => ({ env: env.pub }));

const SECRET = 's'.repeat(40);

beforeEach(() => {
	for (const k of Object.keys(env.priv)) delete env.priv[k];
	env.priv.HUB_SESSION_SECRET = SECRET;
	env.pub.PUBLIC_AUTH_MODE = 'dev';
});
afterEach(() => vi.unstubAllEnvs());

const post = (fields: Record<string, string>) => {
	const body = new FormData();
	for (const [k, v] of Object.entries(fields)) body.set(k, v);
	const set = vi.fn();
	return {
		set,
		run: () =>
			actions.dev!({
				request: new Request('https://c.test/login', { method: 'POST', body }),
				cookies: { set, get: vi.fn(), delete: vi.fn() },
				url: new URL('https://c.test/login')
			} as never)
	};
};

describe('dev mode in a deployment (NODE_ENV=production)', () => {
	beforeEach(() => vi.stubEnv('NODE_ENV', 'production'));

	it('is refused: no provider, no session secret, a login page that says why', async () => {
		expect(authMode()).toBeNull();
		expect(authProvider().id).toBe('none');
		// Even with HUB_SESSION_SECRET set there is no dev fallback and no provider.
		expect(sessionSecret()).toBe(SECRET);
		const out = (await load({
			url: new URL('https://c.test/login'),
			locals: { session: null, sessionEnded: false }
		} as never)) as {
			login: { provider: string; ready: boolean; notice: string; missing: string[] };
		};
		expect(out.login).toMatchObject({
			provider: 'none',
			ready: false,
			missing: ['PUBLIC_AUTH_MODE']
		});
		expect(out.login.notice).toMatch(/only allowed in mock builds and tests/);
	});

	it('the role picker form cannot sign anyone in, and sets no cookie', async () => {
		const { run, set } = post({ role: 'admin', name: 'Mallory' });
		expect(await run()).toMatchObject({ status: 503 });
		expect(set).not.toHaveBeenCalled();
	});

	it('the server refuses to start', () => {
		expect(() => assertAuthModeAtStartup(() => {})).toThrow(
			/only allowed in mock builds and tests/
		);
	});

	it('a session cookie minted by a dev picker is not accepted, even though it decrypts', async () => {
		const cookie = encodeSession(
			newSession({
				provider: 'dev',
				user: { subject: 'dev|x', name: 'x', email: null, roles: ['admin'], tenants: [] },
				accessToken: null
			}),
			SECRET
		);
		const delete_ = vi.fn();
		const event = {
			cookies: {
				get: (k: string) => (k === 'hub_console_session' ? cookie : undefined),
				set: vi.fn(),
				delete: delete_
			},
			locals: {} as Record<string, unknown>
		};
		await authenticate({ event: event as never, resolve: async () => new Response('ok') });
		expect(event.locals.session).toBeNull();
		expect(event.locals.sessionEnded).toBe(true);
		expect(delete_).toHaveBeenCalledWith('hub_console_session', { path: '/' });
	});
});

describe('a session of the other provider', () => {
	it('an ISFC session is not accepted by a dev deployment, a dev session not by an ISFC one', async () => {
		const mk = (provider: 'dev' | 'isfc') =>
			encodeSession(
				newSession({
					provider,
					user: { subject: 's', name: null, email: null, roles: [], tenants: [] },
					accessToken: null
				}),
				SECRET
			);
		const run = async (mode: string, cookie: string) => {
			env.pub.PUBLIC_AUTH_MODE = mode;
			const event = {
				cookies: {
					get: (k: string) => (k === 'hub_console_session' ? cookie : undefined),
					set: vi.fn(),
					delete: vi.fn()
				},
				request: new Request('https://c.test/'),
				url: new URL('https://c.test/'),
				locals: {} as Record<string, unknown>
			};
			await authenticate({ event: event as never, resolve: async () => new Response('ok') });
			return event.locals.session;
		};
		expect(await run('dev', mk('isfc'))).toBeNull();
		expect(await run('isfc', mk('dev'))).toBeNull();
		expect(await run('dev', mk('dev'))).not.toBeNull();
	});
});

describe('start-up check', () => {
	it('an unset mode starts, logs why, and nobody can sign in', () => {
		delete env.pub.PUBLIC_AUTH_MODE;
		vi.stubEnv('NODE_ENV', 'production');
		const log = vi.fn();
		expect(() => assertAuthModeAtStartup(log)).not.toThrow();
		expect(log).toHaveBeenCalledWith(expect.stringMatching(/PUBLIC_AUTH_MODE is not set/));
	});

	it('isfc starts quietly, and so does dev under NODE_ENV=test', () => {
		const log = vi.fn();
		env.pub.PUBLIC_AUTH_MODE = 'isfc';
		vi.stubEnv('NODE_ENV', 'production');
		assertAuthModeAtStartup(log);
		env.pub.PUBLIC_AUTH_MODE = 'dev';
		vi.stubEnv('NODE_ENV', 'test');
		assertAuthModeAtStartup(log);
		expect(log).not.toHaveBeenCalled();
	});
});

describe('dev mode under NODE_ENV=test', () => {
	it('works: the role picker signs in (mock e2e and unit tests rely on it)', async () => {
		expect(authMode()).toBe('dev');
		const { run, set } = post({ role: 'viewer', name: 'Vik' });
		await expect(run()).rejects.toMatchObject({ status: 303 });
		expect(set).toHaveBeenCalledOnce();
	});
});

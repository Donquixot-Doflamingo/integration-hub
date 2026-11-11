import { describe, expect, it, vi } from 'vitest';
import { AUTH, createFakeIsfc } from '$lib/testing/fake-isfc';
import { newSession } from '$lib/server/auth/session';
import { POST } from './+server';

// HUB-492: logout calls ISFC, then always clears the cookie.
const SECRET = 's'.repeat(40);
const env = vi.hoisted(() => ({ priv: {} as Record<string, string | undefined> }));
vi.mock('$env/dynamic/private', () => ({ env: env.priv }));
vi.mock('$env/dynamic/public', () => ({ env: { PUBLIC_AUTH_MODE: 'isfc' } }));

function run(session: ReturnType<typeof newSession> | null, fetchImpl: typeof fetch) {
	Object.assign(env.priv, { HUB_ISFC_AUTH_URL: AUTH, HUB_SESSION_SECRET: SECRET });
	const deleted: string[] = [];
	const cookies = {
		get: () => undefined,
		set: () => {},
		delete: (n: string) => void deleted.push(n)
	};
	const result = POST({
		cookies,
		locals: { session },
		request: new Request('https://console.test/logout', {
			method: 'POST',
			headers: { 'user-agent': 'UA/1' }
		}),
		url: new URL('https://console.test/logout'),
		fetch: fetchImpl
	} as never);
	return { result, deleted };
}

const isfc = (over = {}) =>
	newSession({
		provider: 'isfc',
		user: { subject: 'u', name: null, email: null, roles: [], tenants: [] },
		accessToken: 'access-1',
		refreshToken: 'refresh-1',
		isfcSessionId: 'sess-9',
		accessExp: Math.floor(Date.now() / 1000) + 600,
		deviceId: 'device-00000000-0000-4000-8000-000000000009',
		...over
	});

describe('POST /logout', () => {
	it('ends the session at ISFC with the access token, then clears the cookie and goes to /login', async () => {
		const f = createFakeIsfc();
		const { result, deleted } = run(isfc(), f.fetch);
		await expect(result).rejects.toMatchObject({ status: 303, location: '/login' });
		const call = f.to('/api/v1/logout')[0]!;
		expect(call.method).toBe('POST');
		expect(call.headers.get('authorization')).toBe('Bearer access-1');
		expect(call.headers.get('x-device-id')).toBe('device-00000000-0000-4000-8000-000000000009');
		expect(call.body).toEqual({ session_id: 'sess-9' });
		expect(deleted).toContain('hub_console_session');
	});

	it('clears the cookie even when ISFC refuses or does not answer', async () => {
		for (const f of [
			createFakeIsfc({ logout: () => new Response('x', { status: 500 }) }).fetch,
			(async () => {
				throw new TypeError('fetch failed');
			}) as never
		]) {
			const { result, deleted } = run(isfc(), f);
			await expect(result).rejects.toMatchObject({ status: 303, location: '/login' });
			expect(deleted).toContain('hub_console_session');
		}
	});

	it('refreshes an expired access token first, and logs out with the new one', async () => {
		const f = createFakeIsfc();
		const { result } = run(isfc({ accessExp: 1 }), f.fetch);
		await expect(result).rejects.toMatchObject({ status: 303 });
		expect(f.to('/oauth2/token')).toHaveLength(1);
		expect(f.to('/api/v1/logout')[0]!.headers.get('authorization')).toMatch(/^Bearer eyJ/);
	});

	it('a dev or missing session just clears the cookie, with no call to ISFC', async () => {
		const f = createFakeIsfc();
		for (const s of [
			null,
			newSession({
				provider: 'dev',
				user: { subject: 'd', name: null, email: null, roles: ['admin'], tenants: [] },
				accessToken: null
			})
		]) {
			const { result, deleted } = run(s, f.fetch);
			await expect(result).rejects.toMatchObject({ location: '/login' });
			expect(deleted).toContain('hub_console_session');
		}
		expect(f.calls).toHaveLength(0);
	});
});

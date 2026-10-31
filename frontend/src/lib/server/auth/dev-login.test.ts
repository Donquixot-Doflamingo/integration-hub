import { isRedirect } from '@sveltejs/kit';
import { describe, expect, it, vi } from 'vitest';
import { actions, load } from '../../../routes/(auth)/login/+page.server';
import { decodeSession, SESSION_COOKIE } from './session';
import { sessionSecret } from './index';

// HUB-253: dev mode (PUBLIC_AUTH_MODE=dev): the login action sets the httpOnly session cookie
// and returns to the requested page.
vi.mock('$env/dynamic/public', () => ({ env: { PUBLIC_AUTH_MODE: 'dev' } }));

function post(fields: Record<string, string>) {
	const body = new FormData();
	for (const [k, v] of Object.entries(fields)) body.set(k, v);
	const set = vi.fn();
	const event = {
		request: new Request('http://c.test/login', { method: 'POST', body }),
		cookies: { set },
		url: new URL('http://c.test/login')
	};
	return { set, run: () => actions.default!(event as never) };
}

describe('dev login', () => {
	it('shows the role picker', () => {
		const out = load({ url: new URL('http://c.test/login'), locals: { session: null } } as never);
		expect(out).toMatchObject({ login: { provider: 'dev', ready: true, missing: [] } });
	});

	it('sets an httpOnly, SameSite=Lax session cookie and redirects to next', async () => {
		const { set, run } = post({ role: 'viewer', name: 'Vik', next: '/dlq?tenant_id=a' });
		try {
			await run();
			expect.unreachable();
		} catch (e) {
			expect(isRedirect(e)).toBe(true);
			expect(e).toMatchObject({ status: 303, location: '/dlq?tenant_id=a' });
		}
		expect(set).toHaveBeenCalledOnce();
		const [name, value, opts] = set.mock.calls[0]!;
		expect(name).toBe(SESSION_COOKIE);
		expect(opts).toMatchObject({ path: '/', httpOnly: true, sameSite: 'lax', secure: false });
		const s = decodeSession(value, sessionSecret()!);
		expect(s).toMatchObject({
			provider: 'dev',
			accessToken: null,
			user: { name: 'Vik', roles: ['viewer'] }
		});
	});

	it('never redirects off-site', async () => {
		const { run } = post({ role: 'viewer', next: '//evil.example' });
		await expect(run()).rejects.toMatchObject({ location: '/' });
	});

	it('keeps the input and names the field on a validation error', async () => {
		const { set, run } = post({ role: 'tenant_viewer', tenants: '', name: 'T' });
		expect(await run()).toMatchObject({
			status: 400,
			data: { field: 'tenants', values: { role: 'tenant_viewer', name: 'T' } }
		});
		expect(set).not.toHaveBeenCalled();
	});
});

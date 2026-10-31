import { describe, expect, expectTypeOf, it, vi } from 'vitest';
import { adminClient, REQUEST_ID_HEADER, resolveBaseUrl, unwrap } from './client';
import { HubApiError } from './errors';
import type { Me } from './types';

const BASE = 'http://hub.test/admin/v1';
const me: Me = {
	subject: '00u1abcd2EFGH3ijk4l5',
	email: 'priya@example.com',
	name: 'Priya Sharma',
	roles: ['editor'],
	tenants: [],
	permissions: ['integrations:publish'],
	token_expires_at: '2026-10-31T10:15:00Z'
};

function json(body: unknown, status = 200, headers: Record<string, string> = {}) {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json', ...headers }
	});
}

function fakeFetch(res: Response | (() => Response | Promise<Response>)) {
	const calls: Request[] = [];
	const fn = vi.fn(async (input: RequestInfo | URL) => {
		calls.push(input as Request);
		return typeof res === 'function' ? res() : res.clone();
	});
	return { fn: fn as unknown as typeof fetch, calls };
}

describe('adminClient', () => {
	it('sends the bearer token and resolves the base URL', async () => {
		const f = fakeFetch(json(me));
		const api = adminClient({ baseUrl: BASE, fetch: f.fn, token: 'tok_123' });
		await api.call((c) => c.GET('/me'));
		expect(f.calls[0]?.url).toBe(`${BASE}/me`);
		expect(f.calls[0]?.headers.get('Authorization')).toBe('Bearer tok_123');
	});

	it('reads the token lazily and omits the header without one', async () => {
		const f = fakeFetch(json(me));
		const session: { token?: string } = {};
		const api = adminClient({ baseUrl: BASE, fetch: f.fn, token: () => session.token });
		await api.call((c) => c.GET('/me'));
		session.token = 'later';
		await api.call((c) => c.GET('/me'));
		expect(f.calls[0]?.headers.has('Authorization')).toBe(false);
		expect(f.calls[1]?.headers.get('Authorization')).toBe('Bearer later');
	});

	it('returns typed data and the X-Hub-Request-Id on success', async () => {
		const f = fakeFetch(json(me, 200, { [REQUEST_ID_HEADER]: 'req_01J9Z3M1A5B7C9D1E3F5G7H9KA' }));
		const api = adminClient({ baseUrl: BASE, fetch: f.fn });
		const res = await api.call((c) => c.GET('/me'));
		expectTypeOf(res).toExtend<{ ok: boolean }>();
		if (!res.ok) throw new Error('expected ok');
		expectTypeOf(res.data).toEqualTypeOf<Me>();
		expect(res.data.roles).toEqual(['editor']);
		expect(res.requestId).toBe('req_01J9Z3M1A5B7C9D1E3F5G7H9KA');
		expect(unwrap(res)).toEqual(me);
	});

	it('builds a HubApiError from the canonical envelope (body request_id wins)', async () => {
		const envelope = {
			error: {
				code: 'FORBIDDEN',
				message: "Requires role 'admin'",
				retryable: false,
				retry_after_ms: null,
				details: [],
				meta: { required_role: 'admin' },
				request_id: 'req_01J9Z3M1A5B7C9D1E3F5G7H9KE'
			}
		};
		const f = fakeFetch(json(envelope, 403, { [REQUEST_ID_HEADER]: 'req_HEADER' }));
		const api = adminClient({ baseUrl: BASE, fetch: f.fn });
		const res = await api.call((c) => c.GET('/me'));
		expect(res.ok).toBe(false);
		if (res.ok) return;
		expect(res.error).toBeInstanceOf(HubApiError);
		expect(res.error).toMatchObject({
			status: 403,
			code: 'FORBIDDEN',
			message: "Requires role 'admin'",
			retryable: false,
			requestId: 'req_01J9Z3M1A5B7C9D1E3F5G7H9KE',
			meta: { required_role: 'admin' }
		});
		expect(res.error.is('FORBIDDEN', 'UNAUTHORIZED')).toBe(true);
		expect(res.requestId).toBe('req_01J9Z3M1A5B7C9D1E3F5G7H9KE');
		expect(() => unwrap(res)).toThrow(HubApiError);
	});

	it.each([
		{
			name: 'HTML from a proxy',
			res: () =>
				new Response('<html>Bad gateway</html>', {
					status: 502,
					statusText: 'Bad Gateway',
					headers: { 'Content-Type': 'text/html', [REQUEST_ID_HEADER]: 'req_FROM_HEADER' }
				}),
			want: {
				status: 502,
				code: 'UNEXPECTED_RESPONSE',
				retryable: true,
				requestId: 'req_FROM_HEADER'
			}
		},
		{
			name: 'JSON without an envelope',
			res: () => json({ oops: true }, 400),
			want: { status: 400, code: 'UNEXPECTED_RESPONSE', retryable: false, requestId: null }
		},
		{
			name: 'an unknown code from a newer server',
			res: () =>
				json(
					{
						error: {
							code: 'SOMETHING_NEW',
							message: 'new',
							retryable: true,
							request_id: 'req_01J9Z3M1A5B7C9D1E3F5G7H9KF'
						}
					},
					409
				),
			want: { status: 409, code: 'SOMETHING_NEW', retryable: true }
		}
	])('maps $name', async ({ res, want }) => {
		const api = adminClient({ baseUrl: BASE, fetch: fakeFetch(res).fn });
		const r = await api.call((c) => c.GET('/me'));
		if (r.ok) throw new Error('expected error');
		expect(r.error).toMatchObject(want);
	});

	describe('X-Hub-Request-Id (declared on every hub-admin response since 1.1.0, HUB-278)', () => {
		const envelope = (request_id: string) => ({
			error: { code: 'CONFLICT', message: 'Version moved', retryable: false, request_id }
		});

		it('takes the header when the envelope has no request id', async () => {
			const api = adminClient({
				baseUrl: BASE,
				fetch: fakeFetch(
					json(envelope(''), 409, { [REQUEST_ID_HEADER]: 'req_01J9Z3M1A5B7C9D1E3F5G7H9KG' })
				).fn
			});
			const r = await api.call((c) => c.GET('/me'));
			if (r.ok) throw new Error('expected error');
			expect(r.requestId).toBe('req_01J9Z3M1A5B7C9D1E3F5G7H9KG');
			expect(r.error.requestId).toBe('req_01J9Z3M1A5B7C9D1E3F5G7H9KG');
		});

		it('is null on a success when a proxy dropped the header', async () => {
			const api = adminClient({ baseUrl: BASE, fetch: fakeFetch(json(me, 200)).fn });
			const r = await api.call((c) => c.GET('/me'));
			expect(r).toMatchObject({ ok: true, requestId: null });
		});

		it('treats an empty header as missing', async () => {
			const api = adminClient({
				baseUrl: BASE,
				fetch: fakeFetch(json(me, 200, { [REQUEST_ID_HEADER]: '' })).fn
			});
			expect(await api.call((c) => c.GET('/me'))).toMatchObject({ ok: true, requestId: null });
		});
	});

	it('maps a thrown fetch to NETWORK_ERROR with status 0', async () => {
		const api = adminClient({
			baseUrl: BASE,
			fetch: fakeFetch(() => Promise.reject(new TypeError('Failed to fetch'))).fn
		});
		const r = await api.call((c) => c.GET('/me'));
		if (r.ok) throw new Error('expected error');
		expect(r.status).toBe(0);
		expect(r.error).toMatchObject({ code: 'NETWORK_ERROR', retryable: true, requestId: null });
		expect(r.error.cause).toBeInstanceOf(TypeError);
	});
});

describe('resolveBaseUrl', () => {
	it.each([
		{
			base: 'https://hub-admin.staging.internal/admin/v1/',
			origin: undefined,
			want: 'https://hub-admin.staging.internal/admin/v1'
		},
		{ base: '/admin/v1', origin: 'http://localhost:5173', want: 'http://localhost:5173/admin/v1' },
		{
			base: '/admin/v1',
			origin: 'https://console.internal',
			want: 'https://console.internal/admin/v1'
		}
	])('$base @ $origin', ({ base, origin, want }) => {
		expect(resolveBaseUrl(base, origin)).toBe(want);
	});
});

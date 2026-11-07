import { describe, expect, it, vi } from 'vitest';

// HUB-286: the same-origin admin API proxy that lets the console run against a real Hub.
const env: Record<string, string | undefined> = {};
vi.mock('$env/dynamic/private', () => ({ env }));

const {
	adminUpstream,
	filterSerializedResponseHeaders,
	forwardToAdmin,
	isAdminPath,
	proxyAdminApi,
	sameOrigin
} = await import('./admin-proxy');

const UPSTREAM = 'http://127.0.0.1:8081';
const TOKEN = 'eyJ.session-token.sig';

function event(
	path: string,
	init: RequestInit & { headers?: Record<string, string> } = {},
	opts: { token?: string | null; sub?: boolean } = {}
) {
	const url = new URL(`http://localhost:5173${path}`);
	return {
		request: new Request(url, init),
		url,
		isSubRequest: opts.sub ?? false,
		locals: {
			session: opts.token === null ? null : { accessToken: opts.token ?? TOKEN }
		}
	} as never;
}

function upstreamOk(body: unknown = { data: [] }, headers: Record<string, string> = {}) {
	return vi.fn<typeof fetch>(async () =>
		Response.json(body, {
			status: 200,
			headers: {
				etag: '"v7"',
				'x-hub-request-id': 'req_01J9Z3K8X2V7Q4M6N8P0R2S4T6',
				'set-cookie': 'upstream=1',
				server: 'hub',
				...headers
			}
		})
	);
}

describe('isAdminPath', () => {
	it.each([
		['/admin/v1', true],
		['/admin/v1/integrations', true],
		['/admin/v1/integrations/int_a/versions?limit=5', true],
		['/admin/v10', false],
		['/admin', false],
		['/integrations', false]
	])('%s -> %s', (p, want) => expect(isAdminPath(p.split('?')[0]!)).toBe(want));
});

describe('sameOrigin', () => {
	it.each([
		['GET from anywhere', 'GET', { origin: 'https://evil.test' }, false, true],
		['POST same Origin', 'POST', { origin: 'http://localhost:5173' }, false, true],
		['POST foreign Origin', 'POST', { origin: 'https://evil.test' }, false, false],
		['POST no Origin, same-origin fetch', 'POST', { 'sec-fetch-site': 'same-origin' }, false, true],
		['POST no Origin, cross-site fetch', 'POST', { 'sec-fetch-site': 'cross-site' }, false, false],
		['POST no headers at all', 'POST', {}, false, false],
		['DELETE from a form action (sub-request)', 'DELETE', {}, true, true]
	])('%s', (_name, method, headers, sub, want) => {
		const e = event('/admin/v1/tenants', { method, headers }, { sub });
		expect(sameOrigin(e)).toBe(want);
	});
});

describe('forwardToAdmin', () => {
	it('adds the session token, never the browser credentials, and keeps path and query', async () => {
		const f = upstreamOk();
		const res = await forwardToAdmin(
			event('/admin/v1/requests?tenant_id=orders-service&limit=50', {
				headers: {
					authorization: 'Bearer forged',
					cookie: 'hub_session=abc',
					'if-none-match': '"v6"',
					'x-forwarded-for': '10.0.0.1',
					accept: 'application/json'
				}
			}),
			UPSTREAM,
			f
		);
		expect(res.status).toBe(200);
		const [url, init] = f.mock.calls[0]!;
		expect(url).toBe(`${UPSTREAM}/admin/v1/requests?tenant_id=orders-service&limit=50`);
		const sent = init!.headers as Headers;
		expect(sent.get('authorization')).toBe(`Bearer ${TOKEN}`);
		expect(sent.get('cookie')).toBeNull();
		expect(sent.get('x-forwarded-for')).toBeNull();
		expect(sent.get('if-none-match')).toBe('"v6"');
		expect(init!.method).toBe('GET');
		expect(init!.body).toBeUndefined();
		expect(init!.redirect).toBe('manual');
	});

	it('without a session it sends no authorization at all (the API answers 401)', async () => {
		const f = upstreamOk();
		await forwardToAdmin(
			event('/admin/v1/me', { headers: { authorization: 'Bearer forged' } }, { token: null }),
			UPSTREAM,
			f
		);
		expect((f.mock.calls[0]![1]!.headers as Headers).get('authorization')).toBeNull();
	});

	it('passes a mutation body and its preconditions through', async () => {
		const f = upstreamOk({ ok: true });
		const body = JSON.stringify({ reason: 'rotate after the incident review' });
		await forwardToAdmin(
			event('/admin/v1/integrations/int_a/pause', {
				method: 'POST',
				body,
				headers: {
					origin: 'http://localhost:5173',
					'content-type': 'application/json',
					'if-match': '"v7"'
				}
			}),
			UPSTREAM,
			f
		);
		const init = f.mock.calls[0]![1]!;
		expect(init.method).toBe('POST');
		expect(new TextDecoder().decode(init.body as ArrayBuffer)).toBe(body);
		expect((init.headers as Headers).get('if-match')).toBe('"v7"');
		expect((init.headers as Headers).get('content-type')).toBe('application/json');
	});

	it('refuses a cross-origin mutation before it reaches the Hub', async () => {
		const f = upstreamOk();
		const res = await forwardToAdmin(
			event('/admin/v1/tenants/orders-service', {
				method: 'DELETE',
				headers: { origin: 'https://evil.test' }
			}),
			UPSTREAM,
			f
		);
		expect(res.status).toBe(403);
		expect(f).not.toHaveBeenCalled();
	});

	it('returns only the contract headers of the answer', async () => {
		const res = await forwardToAdmin(event('/admin/v1/integrations/int_a'), UPSTREAM, upstreamOk());
		expect(res.headers.get('etag')).toBe('"v7"');
		expect(res.headers.get('x-hub-request-id')).toBe('req_01J9Z3K8X2V7Q4M6N8P0R2S4T6');
		expect(res.headers.get('content-type')).toMatch(/application\/json/);
		expect(res.headers.get('set-cookie')).toBeNull();
		expect(res.headers.get('server')).toBeNull();
	});

	it('keeps the API status and body (errors pass through unchanged)', async () => {
		const envelope = { error: { code: 'FORBIDDEN', message: 'needs admin' } };
		const f = vi.fn<typeof fetch>(async () => Response.json(envelope, { status: 403 }));
		const res = await forwardToAdmin(event('/admin/v1/tenants'), UPSTREAM, f);
		expect(res.status).toBe(403);
		expect(await res.json()).toEqual(envelope);
	});

	it('answers 502 with a hint when the admin role is down', async () => {
		const f = vi.fn<typeof fetch>(async () => {
			throw new TypeError('fetch failed');
		});
		const res = await forwardToAdmin(event('/admin/v1/me'), UPSTREAM, f);
		expect(res.status).toBe(502);
		expect(await res.text()).toContain('Is the hub admin role running?');
	});
});

describe('proxyAdminApi', () => {
	const resolve = vi.fn(async () => new Response('page'));

	it('passes through when HUB_ADMIN_UPSTREAM is unset (mock mode, or a front proxy routes /admin/v1)', async () => {
		env.HUB_ADMIN_UPSTREAM = undefined;
		expect(adminUpstream()).toBeNull();
		const res = await proxyAdminApi({ event: event('/admin/v1/me'), resolve } as never);
		expect(await res.text()).toBe('page');
	});

	it('passes non-admin paths through even with an upstream', async () => {
		env.HUB_ADMIN_UPSTREAM = `${UPSTREAM}/`;
		expect(adminUpstream()).toBe(UPSTREAM);
		const res = await proxyAdminApi({ event: event('/integrations'), resolve } as never);
		expect(await res.text()).toBe('page');
	});
});

describe('filterSerializedResponseHeaders', () => {
	it.each([
		['x-hub-request-id', true],
		['X-Hub-Request-Id', true],
		['etag', true],
		['set-cookie', false],
		['content-type', false]
	])('%s -> %s', (name, want) => expect(filterSerializedResponseHeaders(name)).toBe(want));
});

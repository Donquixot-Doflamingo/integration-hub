import { describe, expect, it, vi } from 'vitest';
import { forwardToAdmin } from './admin-proxy';
import { isTokenRefusal, normalizeGatewayError } from './gateway-errors';

// HUB-492 / HUB-494: ISFC gateway answers through the admin proxy.
vi.mock('$env/dynamic/private', () => ({ env: {} }));

const UPSTREAM = 'https://gw.isfc.test/hub';

function event(token = 'tok-old', init: RequestInit = {}) {
	const url = new URL('http://localhost:5173/admin/v1/vendors');
	return {
		request: new Request(url, init),
		url,
		isSubRequest: false,
		locals: { session: { accessToken: token } }
	} as never;
}

const gw = (status: number, error_code: string, extra: object = {}) =>
	Response.json({ error_code, error: 'raw gateway text', message: 'raw', ...extra }, { status });

describe('normalizeGatewayError', () => {
	it.each([
		[401, 'GW_007', 'UNAUTHORIZED', /session has ended/],
		[403, 'GW_015', 'FORBIDDEN', /no access to that tenant/],
		[403, 'AS_FB_21', 'FORBIDDEN', /no tenant access/],
		[401, 'GW_004', 'UNAUTHORIZED', /sign-in is not valid/],
		[429, 'GW_014', 'RATE_LIMITED', /Too many requests/],
		[400, 'GW_016', 'VALIDATION_FAILED', /tenant is not valid/]
	])('%s %s becomes a Hub %s envelope with a fixed message', async (status, code, hub, text) => {
		const out = await normalizeGatewayError(gw(status, code, { request_id: 'gw-req-1' }));
		expect(out.status).toBe(status);
		const body = (await out.json()) as { error: Record<string, unknown> };
		expect(body.error).toMatchObject({
			code: hub,
			message: expect.stringMatching(text),
			details: [],
			meta: { isfc_error_code: code }
		});
		expect(JSON.stringify(body)).not.toContain('raw gateway text');
		expect(out.headers.get('x-hub-request-id')).toBe('gw-req-1');
	});

	it('leaves a Hub envelope, success and non-JSON answers alone', async () => {
		const hub = Response.json(
			{
				error: { code: 'FORBIDDEN', message: 'needs admin', retryable: false, request_id: 'req_x' }
			},
			{ status: 403 }
		);
		expect(await normalizeGatewayError(hub)).toBe(hub);
		const ok = Response.json({ items: [] });
		expect(await normalizeGatewayError(ok)).toBe(ok);
		const html = new Response('<html>', { status: 502, headers: { 'content-type': 'text/html' } });
		expect(await normalizeGatewayError(html)).toBe(html);
	});

	it('does not rewrite an unknown code format (nothing is echoed)', async () => {
		const odd = gw(403, '<script>');
		const out = await normalizeGatewayError(odd);
		expect(out).toBe(odd);
	});

	it('knows which answers mean "the token is no longer good"', () => {
		expect(isTokenRefusal(401, { error_code: 'GW_004' })).toBe(true);
		expect(isTokenRefusal(401, { error_code: 'GW_002' })).toBe(true);
		expect(isTokenRefusal(401, { error_code: 'GW_007' })).toBe(false);
		expect(isTokenRefusal(403, { error_code: 'GW_004' })).toBe(false);
		expect(isTokenRefusal(401, { error: { code: 'UNAUTHORIZED' } })).toBe(false);
	});
});

describe('forwardToAdmin with an ISFC session', () => {
	it('refreshes once on GW_004 and repeats the call with the new token and the same body', async () => {
		const seen: { auth: string | null; body: string }[] = [];
		const answers = [gw(401, 'GW_004'), Response.json({ ok: true }, { headers: { etag: '"v1"' } })];
		const f = vi.fn<typeof fetch>(async (_u, init) => {
			seen.push({
				auth: (init!.headers as Headers).get('authorization'),
				body: new TextDecoder().decode(init!.body as ArrayBuffer)
			});
			return answers.shift()!;
		});
		const refreshToken = vi.fn(async () => 'tok-new');
		const res = await forwardToAdmin(
			event('tok-old', {
				method: 'POST',
				body: '{"reason":"x"}',
				headers: { origin: 'http://localhost:5173', 'content-type': 'application/json' }
			}),
			UPSTREAM,
			f,
			{ refreshToken }
		);
		expect(res.status).toBe(200);
		expect(refreshToken).toHaveBeenCalledOnce();
		expect(seen).toEqual([
			{ auth: 'Bearer tok-old', body: '{"reason":"x"}' },
			{ auth: 'Bearer tok-new', body: '{"reason":"x"}' }
		]);
	});

	it('does not loop: a second refusal is the answer, as a Hub error', async () => {
		const f = vi.fn<typeof fetch>(async () => gw(401, 'GW_004'));
		const refreshToken = vi.fn(async () => 'tok-new');
		const res = await forwardToAdmin(event(), UPSTREAM, f, { refreshToken });
		expect(f).toHaveBeenCalledTimes(2);
		expect(refreshToken).toHaveBeenCalledOnce();
		expect(res.status).toBe(401);
		expect(await res.json()).toMatchObject({ error: { code: 'UNAUTHORIZED' } });
	});

	it('when the session cannot be refreshed the refusal is passed on after one call', async () => {
		const f = vi.fn<typeof fetch>(async () => gw(401, 'GW_004'));
		const res = await forwardToAdmin(event(), UPSTREAM, f, { refreshToken: async () => null });
		expect(f).toHaveBeenCalledOnce();
		expect(res.status).toBe(401);
	});

	it('does not refresh for other 401s or other statuses', async () => {
		for (const answer of [gw(401, 'GW_007'), gw(403, 'GW_015'), Response.json({ ok: 1 })]) {
			const f = vi.fn<typeof fetch>(async () => answer);
			const refreshToken = vi.fn(async () => 'x');
			await forwardToAdmin(event(), UPSTREAM, f, { refreshToken });
			expect(refreshToken).not.toHaveBeenCalled();
			expect(f).toHaveBeenCalledOnce();
		}
	});

	it('a GW_015 reaches the console as a FORBIDDEN with the tenant message', async () => {
		const f = vi.fn<typeof fetch>(async () => gw(403, 'GW_015'));
		const res = await forwardToAdmin(event(), UPSTREAM, f);
		expect(res.status).toBe(403);
		expect(await res.json()).toMatchObject({
			error: { code: 'FORBIDDEN', message: expect.stringMatching(/no access to that tenant/) }
		});
	});
});

// HUB-494: the proxy sends the session's tenant and learns the gateway's default.
describe('X-Tenant-Id through the proxy', () => {
	const TENANT = '22222222-2222-4222-8222-222222222222';
	const ECHO = '11111111-1111-4111-8111-111111111111';

	function ev(session: object | null) {
		const url = new URL('http://localhost:5173/admin/v1/me');
		return {
			request: new Request(url),
			url,
			isSubRequest: false,
			locals: { session }
		} as never;
	}

	it('sends the tenant of the session next to the token', async () => {
		const f = vi.fn<typeof fetch>(async () => Response.json({}));
		await forwardToAdmin(ev({ accessToken: 't', tenantId: TENANT }), UPSTREAM, f);
		expect((f.mock.calls[0]![1]!.headers as Headers).get('x-tenant-id')).toBe(TENANT);
	});

	it('never forwards an X-Tenant-Id from the browser, and sends none without a chosen tenant', async () => {
		const f = vi.fn<typeof fetch>(async () => Response.json({}));
		const url = new URL('http://localhost:5173/admin/v1/me');
		const forged = {
			request: new Request(url, { headers: { 'x-tenant-id': TENANT } }),
			url,
			isSubRequest: false,
			locals: { session: { accessToken: 't', tenantId: null } }
		} as never;
		await forwardToAdmin(forged, UPSTREAM, f);
		expect((f.mock.calls[0]![1]!.headers as Headers).get('x-tenant-id')).toBeNull();
	});

	it('does not send a tenant that is not a UUID (the gateway would answer GW_016)', async () => {
		const f = vi.fn<typeof fetch>(async () => Response.json({}));
		await forwardToAdmin(ev({ accessToken: 't', tenantId: 'orders-service' }), UPSTREAM, f);
		expect((f.mock.calls[0]![1]!.headers as Headers).get('x-tenant-id')).toBeNull();
	});

	it('adopts the tenant the gateway used when none was sent', async () => {
		const f = vi.fn<typeof fetch>(async () =>
			Response.json({}, { headers: { 'x-isfc-tenant-id': ECHO.toUpperCase() } })
		);
		const onTenantEcho = vi.fn();
		await forwardToAdmin(ev({ accessToken: 't', tenantId: null }), UPSTREAM, f, { onTenantEcho });
		expect(onTenantEcho).toHaveBeenCalledExactlyOnceWith(ECHO);
	});

	it('does not let a late answer undo a switch: no adoption when a tenant was sent', async () => {
		const f = vi.fn<typeof fetch>(async () =>
			Response.json({}, { headers: { 'x-isfc-tenant-id': ECHO } })
		);
		const onTenantEcho = vi.fn();
		await forwardToAdmin(ev({ accessToken: 't', tenantId: TENANT }), UPSTREAM, f, { onTenantEcho });
		expect(onTenantEcho).not.toHaveBeenCalled();
	});

	it('ignores an echo that is not a UUID, and does not forward the header to the page', async () => {
		const f = vi.fn<typeof fetch>(async () =>
			Response.json({}, { headers: { 'x-isfc-tenant-id': 'evil<script>' } })
		);
		const onTenantEcho = vi.fn();
		const res = await forwardToAdmin(ev({ accessToken: 't', tenantId: null }), UPSTREAM, f, {
			onTenantEcho
		});
		expect(onTenantEcho).not.toHaveBeenCalled();
		expect(res.headers.get('x-isfc-tenant-id')).toBeNull();
	});
});

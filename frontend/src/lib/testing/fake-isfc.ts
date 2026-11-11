import { createHash } from 'node:crypto';

/**
 * A fake ISFC for tests (AUTH_PLAN §18: tests never call a real ISFC or Entra). It answers the
 * few endpoints the console uses, with ISFC's envelopes, and records every call so tests can
 * assert the exact request shapes (headers, bodies). Pass `fake.fetch` wherever the code takes a
 * `fetch`.
 */
export const AUTH = 'https://auth.isfc.test';
export const GATEWAY = 'https://gw.isfc.test';

export const PLATFORM_TENANT = '11111111-1111-4111-8111-111111111111';
export const ORDERS_TENANT = '22222222-2222-4222-8222-222222222222';
export const BILLING_TENANT = '33333333-3333-4333-8333-333333333333';

/** An unsigned JWT (the console reads claims for display and expiry only; the gateway verifies). */
export function jwt(claims: Record<string, unknown>): string {
	const b = (o: unknown) => Buffer.from(JSON.stringify(o)).toString('base64url');
	return `${b({ alg: 'RS256', typ: 'JWT' })}.${b(claims)}.sig`;
}

export interface Call {
	method: string;
	url: URL;
	headers: Headers;
	body: unknown;
}

export interface FakeIsfcOptions {
	/** `data` of GET /oauth2/method/<client>. */
	methods?: { id: string; name: string; meta_data?: Record<string, unknown> }[];
	/** Answers POST /oauth2/token; default: a fresh token set. */
	token?: (call: Call, n: number) => Response | Promise<Response>;
	logout?: (call: Call) => Response | Promise<Response>;
	panels?: (call: Call) => Response | Promise<Response>;
}

export const DEFAULT_METHODS = [
	{ id: 'GOOGLE_SSO', name: 'Google', meta_data: { client_id: 'g' } },
	{
		id: 'MICROSOFT_SSO',
		name: 'Microsoft',
		meta_data: {
			client_id: 'entra-client',
			redirect_uri: `${AUTH}/oauth2/callback`,
			tenant_id: 'entra-tenant-id',
			domain_hint: 'example.com'
		}
	},
	{ id: 'EMAIL_OTP', name: 'Email OTP' },
	{ id: 'PHONE_OTP', name: 'Phone' },
	{ id: 'TRUECALLER', name: 'Truecaller' }
];

export function tokenResponse(
	over: Record<string, unknown> = {},
	claims: Record<string, unknown> = {}
): Response {
	const sub = 'user-1';
	return Response.json({
		success: true,
		data: {
			access_token: jwt({
				sub,
				email: 'priya@example.com',
				name: 'Priya Sharma',
				exp: 4102444800,
				...claims
			}),
			refresh_token: 'refresh-1',
			session_id: 'sess-1',
			expires_in: 900,
			token_type: 'Bearer',
			...over
		}
	});
}

export function isfcError(status: number, error_code: string, message = 'denied'): Response {
	return Response.json({ error_code, error: message, message }, { status });
}

export function createFakeIsfc(opts: FakeIsfcOptions = {}) {
	const calls: Call[] = [];
	let tokenCalls = 0;
	const fetchImpl = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
		const req = new Request(input, init);
		const url = new URL(req.url);
		const text = await req.text();
		const call: Call = {
			method: req.method,
			url,
			headers: req.headers,
			body: text ? JSON.parse(text) : undefined
		};
		calls.push(call);
		const path = url.pathname;
		if (url.origin === AUTH) {
			if (req.method === 'GET' && path.startsWith('/oauth2/method/')) {
				return Response.json({ success: true, data: opts.methods ?? DEFAULT_METHODS });
			}
			if (req.method === 'GET' && path === '/oauth2/auth') {
				return Response.json({ success: true, data: { flow_token: 'flow-123', methods: [] } });
			}
			if (req.method === 'POST' && path === '/oauth2/token') {
				return (opts.token ?? (() => tokenResponse()))(call, ++tokenCalls);
			}
			if (req.method === 'POST' && path === '/api/v1/logout') {
				return (opts.logout ?? (() => Response.json({ success: true })))(call);
			}
		}
		if (url.origin === GATEWAY && path === '/auth/oauth2/panels') {
			return (opts.panels ?? (() => panelsResponse()))(call);
		}
		return new Response('not found', { status: 404 });
	};
	return {
		fetch: fetchImpl as typeof fetch,
		calls,
		/** Calls to a path, in order. */
		to: (path: string) => calls.filter((c) => c.url.pathname === path)
	};
}

export function panelsResponse(
	tenants: { id: string; key: string; name: string; roles?: string[] }[] = [
		{ id: ORDERS_TENANT, key: 'ORDERS', name: 'Orders service', roles: ['HUB_TENANT_ADMIN'] },
		{
			id: PLATFORM_TENANT,
			key: 'INTEGRATION_HUB_PLATFORM',
			name: 'Integration Hub platform',
			roles: ['HUB_ADMIN']
		},
		{ id: BILLING_TENANT, key: 'BILLING', name: 'Billing', roles: ['HUB_TENANT_VIEWER'] }
	]
): Response {
	return Response.json({
		success: true,
		data: {
			panels: [
				{
					client_id: 'integration-hub-console',
					name: 'Integration Hub',
					domain: 'https://console.test',
					tenancy: 'required',
					current: true,
					tenants
				},
				{
					client_id: 'other-panel',
					name: 'Other',
					domain: 'https://o.test',
					tenancy: 'none',
					current: false,
					tenants: []
				}
			]
		}
	});
}

/** S256 challenge of a verifier, to check what the console sent. */
export function s256(verifier: string): string {
	return createHash('sha256').update(verifier).digest('base64url');
}

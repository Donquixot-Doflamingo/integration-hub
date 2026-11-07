import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, type Page } from '@playwright/test';
import { parse } from 'yaml';
import type {
	AccountCreate,
	EndpointRequestSchema,
	GoldenFixture,
	TransformerKind,
	TransformerVersionCreate,
	VendorCreate,
	VendorEndpointCreate
} from '../src/lib/api/types';

/**
 * Registry objects for the Aadhaar e-KYC example (`examples/aadhaar-okyc`, HUB-351) created
 * through the console's same-origin admin proxy as the signed-in admin, so the HUB-292 e2e can
 * build `int_aadhaar_okyc` (one target, `send_otp` on the default pair and `verify_otp` on its
 * own, `TargetRef.operations`) in the form editor. Everything comes from the example's config
 * (ADR-0023, HUB-441): the endpoints' request schemas from its vendor file, the transformers and
 * their samples, and the operations' request schemas (`aadhaarOperations`). The vendor, account
 * and transformers get the run suffix so runs never collide.
 */
const EXAMPLE = join(import.meta.dirname, '..', '..', 'examples', 'aadhaar-okyc');

const yamlFile = (path: string) => parse(readFileSync(path, 'utf8')) as Record<string, unknown>;

/** The request body schema of each operation of the example's int_aadhaar_okyc version 1. */
export function aadhaarOperations(): Record<string, Record<string, unknown>> {
	const version = yamlFile(
		join(EXAMPLE, 'config', 'integrations', 'int_aadhaar_okyc', 'versions', '1.yaml')
	);
	const ops = version.operations as Record<
		string,
		{ request_schema: { body: Record<string, unknown> } }
	>;
	return Object.fromEntries(
		Object.entries(ops).map(([name, op]) => [name, op.request_schema.body])
	);
}

export interface AadhaarObjects {
	vendor: string;
	account: string;
	otpReq: string;
	otpRes: string;
	verifyReq: string;
	verifyRes: string;
}

const json = (path: string) => JSON.parse(readFileSync(path, 'utf8')) as Record<string, unknown>;

/** GoldenFixture[] from `fixtures/<name>.<field>.json` files. */
function fixtures(dir: string): GoldenFixture[] {
	const byName = new Map<string, Record<string, unknown>>();
	for (const file of readdirSync(dir).sort()) {
		const m =
			/^(.+)\.(input|expected_request|vendor_response|expected_response|expected_error|account_config)\.json$/.exec(
				file
			);
		if (!m) continue;
		const f = byName.get(m[1]!) ?? { name: m[1] };
		const value = json(join(dir, file));
		// Config-tree files hold the whole outcome ({"response": ...} or {"error": ...}); the
		// admin API takes expected_response or expected_error (GoldenFixture).
		if (m[2] === 'expected_response' && 'error' in value) f.expected_error = value.error;
		else if (m[2] === 'expected_response' && 'response' in value)
			f.expected_response = value.response;
		else f[m[2]!] = value;
		// GoldenFixture has no field for the vendor session a response transformer opens (CCR-007):
		// such a fixture cannot pass through the API, so it is left out here.
		if (m[2] === 'expected_response' && 'session' in value) f.skip = true;
		byName.set(m[1]!, f);
	}
	return [...byName.values()].filter((f) => !f.skip).map(({ skip: _, ...f }) => f as GoldenFixture);
}

async function post(page: Page, path: string, body: unknown, ok: number[] = [200, 201]) {
	const origin = new URL(page.url()).origin;
	const res = await page.request.post(`${origin}/admin/v1${path}`, {
		data: body,
		headers: { origin, 'content-type': 'application/json' }
	});
	const text = await res.text();
	expect(ok, `POST ${path} -> ${res.status()} ${text.slice(0, 600)}`).toContain(res.status());
	return res.status();
}

export async function createAadhaarObjects(page: Page, run: string): Promise<AadhaarObjects> {
	const o: AadhaarObjects = {
		vendor: `cf-sid-${run}`,
		account: `cf-sid-test-${run}`,
		otpReq: `CF-OKYC-OTP-REQ-${run}`,
		otpRes: `CF-OKYC-OTP-RES-${run}`,
		verifyReq: `CF-OKYC-VERIFY-REQ-${run}`,
		verifyRes: `CF-OKYC-VERIFY-RES-${run}`
	};
	const endpoints = yamlFile(join(EXAMPLE, 'config', 'vendors', 'cashfree-sid.yaml'))
		.endpoints as Record<string, { request_schema: EndpointRequestSchema }>;
	await post(page, '/vendors', {
		vendor_id: o.vendor,
		display_name: 'Cashfree Secure ID (e2e)',
		auth_scheme: 'static_headers',
		regions: { sandbox: 'https://sandbox.cashfree.com/verification' },
		docs_url: 'https://www.cashfree.com/docs/api-reference/vrs/getting-started',
		reason: 'E2E: KYC provider for the Aadhaar example'
	} satisfies VendorCreate);
	for (const [name, path] of [
		['okyc_otp', '/offline-aadhaar/otp'],
		['okyc_verify', '/offline-aadhaar/verify']
	] as const) {
		await post(page, `/vendors/${o.vendor}/endpoints`, {
			name,
			method: 'POST',
			path,
			timeout_ms: 10000,
			connect_timeout_ms: 1000,
			idempotent: false,
			// What the provider accepts: every request sample must fit it (ADR-0023).
			request_schema: endpoints[`${name}@1`]!.request_schema,
			reason: `E2E: ${name} endpoint`
		} satisfies VendorEndpointCreate);
	}
	await post(page, '/accounts', {
		account_id: o.account,
		vendor_id: o.vendor,
		region: 'sandbox',
		config: {},
		quota: { rps: 10, daily: 1000 },
		credentials: {
			client_id: 'e2e-client-id-not-real',
			client_secret: 'e2e-client-secret-not-real'
		},
		reason: 'E2E: test-mode account of the KYC provider'
	} satisfies AccountCreate);
	const ids: [string, string, string][] = [
		['CF-OKYC-OTP-REQ', o.otpReq, 'okyc_otp@1'],
		['CF-OKYC-OTP-RES', o.otpRes, 'okyc_otp@1'],
		['CF-OKYC-VERIFY-REQ', o.verifyReq, 'okyc_verify@1'],
		['CF-OKYC-VERIFY-RES', o.verifyRes, 'okyc_verify@1']
	];
	for (const [from, to, endpoint] of ids) {
		const dir = join(EXAMPLE, 'config', 'transformers', from);
		const yaml = readFileSync(join(dir, '1.yaml'), 'utf8')
			.replace(new RegExp(`^transformer: ${from}$`, 'm'), `transformer: ${to}`)
			.replaceAll('cashfree-sid.', `${o.vendor}.`);
		const head = (key: string) => new RegExp(`^${key}:\\s*(\\S+)`, 'm').exec(yaml)?.[1] ?? '';
		const kind = head('kind') as TransformerKind;
		await post(page, `/transformers/${to}/versions`, {
			kind,
			// A response transformer stands alone: only a request transformer names its endpoint.
			vendor_endpoint: kind === 'request' ? `${o.vendor}.${endpoint}` : null,
			expected_latest_version: null,
			yaml,
			fixtures: fixtures(join(dir, 'fixtures')),
			docs_url: head('docs_url') || null,
			reason: `E2E: ${from} from the Aadhaar example`
		} satisfies TransformerVersionCreate);
	}
	return o;
}

import type { FieldErrors } from '$lib/api/actions';
import type {
	AuthScheme,
	BreakerConfig,
	Vendor,
	VendorCreate,
	VendorDefaults,
	VendorUpdate,
	WebhookVerification
} from '$lib/api/types';

/**
 * Vendor create / edit form (HUB-290), checked against `VendorCreate` / `VendorUpdate` in
 * hub-admin.v1.yaml before anything is sent. The page checks in the browser (Save stays disabled
 * with the reasons listed) and the form action checks again (no-JS path).
 *
 * Field errors are keyed by the JSON pointer of the body property (`/regions`,
 * `/defaults/breaker/window_ms`), the same pointers the API's `VALIDATION_FAILED` details use, so
 * an API error lands on its field (`detailsToPointerFields`).
 *
 * `updateVendor` is a PUT: it replaces every mutable field, so an edit sends the whole form.
 */
export const SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/;
export const ENDPOINT_REF = /^[a-z][a-z0-9_]{0,63}@[1-9][0-9]*$/;
export const DISPLAY_NAME_MAX = 128;

export const AUTH_SCHEMES: readonly AuthScheme[] = [
	'static_headers',
	'bearer',
	'basic',
	'query_param',
	'hmac_signing',
	'oauth2_client_credentials',
	'mtls'
];

export const authSchemeLabels: Record<AuthScheme, string> = {
	static_headers: 'Static headers',
	bearer: 'Bearer token',
	basic: 'Basic auth',
	query_param: 'Query parameter',
	hmac_signing: 'HMAC request signing',
	oauth2_client_credentials: 'OAuth 2 client credentials',
	mtls: 'Mutual TLS'
};

type WebhookScheme = WebhookVerification['scheme'];
export const WEBHOOK_SCHEMES: readonly WebhookScheme[] = [
	'none',
	'hmac_sha256',
	'basic',
	'ip_allowlist'
];
export const webhookSchemeLabels: Record<WebhookScheme, string> = {
	none: 'None (accept unsigned)',
	hmac_sha256: 'HMAC SHA-256 signature header',
	basic: 'Basic auth',
	ip_allowlist: 'Source IP allowlist'
};

/** Breaker defaults, in the order the form shows them, with their contract minimum and kind. */
export const BREAKER_FIELDS = [
	{ key: 'window_ms', label: 'Window (ms)', min: 1000, kind: 'int' },
	{ key: 'min_calls', label: 'Minimum calls', min: 1, kind: 'int' },
	{ key: 'failure_ratio', label: 'Failure ratio (0 to 1)', min: 0, kind: 'ratio' },
	{ key: 'consecutive_timeouts', label: 'Consecutive timeouts', min: 1, kind: 'int' },
	{ key: 'open_ms', label: 'Open for (ms)', min: 1000, kind: 'int' },
	{ key: 'open_max_ms', label: 'Open at most (ms)', min: 1000, kind: 'int' },
	{ key: 'half_open_probes', label: 'Half-open probes', min: 1, kind: 'int' }
] as const satisfies readonly {
	key: keyof BreakerConfig;
	label: string;
	min: number;
	kind: 'int' | 'ratio';
}[];

export type BreakerKey = (typeof BREAKER_FIELDS)[number]['key'];

export interface RegionRow {
	code: string;
	url: string;
}

/** Raw form input (strings as typed). */
export interface FormValues {
	vendor_id: string;
	display_name: string;
	auth_scheme: string;
	regions: RegionRow[];
	docs_url: string;
	per_account_rps: string;
	breaker: Record<BreakerKey, string>;
	probe_endpoint: string;
	probe_dry: boolean;
	probe_every_ms: string;
	/** '' = not set (null). */
	webhook_scheme: string;
	webhook_header: string;
	webhook_max_skew_ms: string;
	/** One entry per line. */
	webhook_ip_allowlist: string;
}

const emptyBreaker = (): Record<BreakerKey, string> =>
	Object.fromEntries(BREAKER_FIELDS.map((f) => [f.key, ''])) as Record<BreakerKey, string>;

export function emptyValues(): FormValues {
	return {
		vendor_id: '',
		display_name: '',
		auth_scheme: 'static_headers',
		regions: [{ code: '', url: '' }],
		docs_url: '',
		per_account_rps: '',
		breaker: emptyBreaker(),
		probe_endpoint: '',
		probe_dry: true,
		probe_every_ms: '',
		webhook_scheme: '',
		webhook_header: '',
		webhook_max_skew_ms: '',
		webhook_ip_allowlist: ''
	};
}

const str = (n: number | null | undefined) => (n == null ? '' : String(n));

export function valuesFromVendor(v: Vendor): FormValues {
	const d = v.defaults ?? {};
	const w = v.webhook_verification ?? null;
	const breaker = emptyBreaker();
	for (const f of BREAKER_FIELDS) breaker[f.key] = str(d.breaker?.[f.key]);
	const regions = Object.entries(v.regions ?? {}).map(([code, url]) => ({ code, url }));
	return {
		vendor_id: v.vendor_id,
		display_name: v.display_name ?? '',
		auth_scheme: v.auth_scheme ?? 'static_headers',
		regions: regions.length ? regions : [{ code: '', url: '' }],
		docs_url: v.docs_url ?? '',
		per_account_rps: str(d.rate_limit?.per_account_rps),
		breaker,
		probe_endpoint: d.health_probe?.endpoint ?? '',
		probe_dry: d.health_probe?.dry ?? true,
		probe_every_ms: str(d.health_probe?.every_ms),
		webhook_scheme: w?.scheme ?? '',
		webhook_header: w?.header ?? '',
		webhook_max_skew_ms: str(w?.max_skew_ms),
		webhook_ip_allowlist: (w?.ip_allowlist ?? []).join('\n')
	};
}

/** Form field names (what the inputs post as). */
export const NAMES = {
	regionCode: 'region_code',
	regionUrl: 'region_url',
	breaker: (k: BreakerKey) => `breaker.${k}`
} as const;

export function valuesFromForm(form: FormData): FormValues {
	const get = (name: string) => {
		const v = form.get(name);
		return typeof v === 'string' ? v : '';
	};
	const codes = form.getAll(NAMES.regionCode).map(String);
	const urls = form.getAll(NAMES.regionUrl).map(String);
	const breaker = emptyBreaker();
	for (const f of BREAKER_FIELDS) breaker[f.key] = get(NAMES.breaker(f.key));
	return {
		vendor_id: get('vendor_id'),
		display_name: get('display_name'),
		auth_scheme: get('auth_scheme'),
		regions: codes.map((code, i) => ({ code, url: urls[i] ?? '' })),
		docs_url: get('docs_url'),
		per_account_rps: get('per_account_rps'),
		breaker,
		probe_endpoint: get('probe_endpoint'),
		probe_dry: form.get('probe_dry') === 'on' || form.get('probe_dry') === 'true',
		probe_every_ms: get('probe_every_ms'),
		webhook_scheme: get('webhook_scheme'),
		webhook_header: get('webhook_header'),
		webhook_max_skew_ms: get('webhook_max_skew_ms'),
		webhook_ip_allowlist: get('webhook_ip_allowlist')
	};
}

/** Every pointer a field of this form answers for (API errors with these land on the field). */
export const POINTERS = [
	'/vendor_id',
	'/display_name',
	'/auth_scheme',
	'/regions',
	'/docs_url',
	'/defaults',
	'/defaults/rate_limit',
	'/defaults/rate_limit/per_account_rps',
	'/defaults/breaker',
	...BREAKER_FIELDS.map((f) => `/defaults/breaker/${f.key}`),
	'/defaults/health_probe',
	'/defaults/health_probe/endpoint',
	'/defaults/health_probe/every_ms',
	'/webhook_verification',
	'/webhook_verification/scheme',
	'/webhook_verification/header',
	'/webhook_verification/max_skew_ms',
	'/webhook_verification/ip_allowlist'
] as const;

const INT = /^\d+$/;
const NUM = /^\d+(\.\d+)?$/;

/** An absolute http(s) URL (contract `format: uri`; the Hub calls it, so it must be a base URL). */
export function isHttpUrl(s: string): boolean {
	try {
		const u = new URL(s);
		return (u.protocol === 'https:' || u.protocol === 'http:') && !!u.hostname;
	} catch {
		return false;
	}
}

/** Problems with the values, keyed by pointer; empty when the form can be sent. */
export function problems(v: FormValues, mode: 'create' | 'edit'): FieldErrors {
	const out: FieldErrors = {};
	if (mode === 'create') {
		const id = v.vendor_id.trim();
		if (!id) out['/vendor_id'] = 'Enter a vendor id.';
		else if (!SLUG.test(id))
			out['/vendor_id'] =
				'3 to 64 characters: letters, digits, "_" or "-", starting with a letter or digit.';
	}
	const name = v.display_name.trim();
	if (!name) out['/display_name'] = 'Enter a display name.';
	else if (name.length > DISPLAY_NAME_MAX)
		out['/display_name'] = `Use at most ${DISPLAY_NAME_MAX} characters (${name.length} now).`;
	if (!(AUTH_SCHEMES as readonly string[]).includes(v.auth_scheme))
		out['/auth_scheme'] = 'Pick an auth scheme.';

	const rows = v.regions.filter((r) => r.code.trim() || r.url.trim());
	if (!rows.length) out['/regions'] = 'Add at least one region with its base URL.';
	else {
		const seen = new Set<string>();
		for (const r of rows) {
			const code = r.code.trim();
			if (!code) out['/regions'] ??= 'Every base URL needs a region code (for example "in").';
			else if (seen.has(code)) out['/regions'] ??= `Region "${code}" is listed twice.`;
			else if (!isHttpUrl(r.url.trim()))
				out['/regions'] ??= `Region "${code}": enter an absolute http(s) base URL.`;
			seen.add(code);
		}
	}
	if (v.docs_url.trim() && !isHttpUrl(v.docs_url.trim()))
		out['/docs_url'] = 'Enter an absolute http(s) URL, or leave it empty.';

	const rps = v.per_account_rps.trim();
	if (rps && !NUM.test(rps))
		out['/defaults/rate_limit/per_account_rps'] = 'A number of requests per second, 0 or more.';

	for (const f of BREAKER_FIELDS) {
		const raw = v.breaker[f.key].trim();
		if (!raw) continue;
		const ptr = `/defaults/breaker/${f.key}`;
		if (f.kind === 'ratio') {
			if (!NUM.test(raw) || Number(raw) > 1) out[ptr] = 'A ratio between 0 and 1, e.g. 0.5.';
		} else if (!INT.test(raw) || Number(raw) < f.min) {
			out[ptr] = `A whole number, at least ${f.min.toLocaleString('en-US')}.`;
		}
	}

	const probe = v.probe_endpoint.trim();
	if (probe && !ENDPOINT_REF.test(probe))
		out['/defaults/health_probe/endpoint'] = 'An endpoint reference such as upload@1.';
	const every = v.probe_every_ms.trim();
	if (every && (!INT.test(every) || Number(every) < 1000))
		out['/defaults/health_probe/every_ms'] = 'A whole number of milliseconds, at least 1,000.';

	if (v.webhook_scheme && !(WEBHOOK_SCHEMES as readonly string[]).includes(v.webhook_scheme))
		out['/webhook_verification/scheme'] = 'Pick a verification scheme.';
	const skew = v.webhook_max_skew_ms.trim();
	if (v.webhook_scheme && skew && !INT.test(skew))
		out['/webhook_verification/max_skew_ms'] = 'A whole number of milliseconds, 0 or more.';
	if (v.webhook_scheme === 'hmac_sha256' && !v.webhook_header.trim())
		out['/webhook_verification/header'] = 'Name the header that carries the signature.';
	if (v.webhook_scheme === 'ip_allowlist' && !ipList(v).length)
		out['/webhook_verification/ip_allowlist'] = 'List at least one address or range, one per line.';
	return out;
}

const ipList = (v: FormValues) =>
	v.webhook_ip_allowlist
		.split('\n')
		.map((s) => s.trim())
		.filter(Boolean);

/** The mutable fields as the API takes them (shared by create and update). */
function fields(v: FormValues): Omit<VendorUpdate, 'reason'> {
	const regions: Record<string, string> = {};
	for (const r of v.regions) if (r.code.trim()) regions[r.code.trim()] = r.url.trim();
	const out: Omit<VendorUpdate, 'reason'> = {
		display_name: v.display_name.trim(),
		auth_scheme: v.auth_scheme as AuthScheme,
		regions,
		docs_url: v.docs_url.trim() || null,
		webhook_verification: webhook(v)
	};
	const d = defaults(v);
	if (d) out.defaults = d;
	return out;
}

function defaults(v: FormValues): VendorDefaults | undefined {
	const d: VendorDefaults = {};
	if (v.per_account_rps.trim()) d.rate_limit = { per_account_rps: Number(v.per_account_rps) };
	const b: BreakerConfig = {};
	for (const f of BREAKER_FIELDS) {
		const raw = v.breaker[f.key].trim();
		if (raw) b[f.key] = Number(raw);
	}
	if (Object.keys(b).length) d.breaker = b;
	if (v.probe_endpoint.trim() || v.probe_every_ms.trim()) {
		d.health_probe = { dry: v.probe_dry };
		if (v.probe_endpoint.trim()) d.health_probe.endpoint = v.probe_endpoint.trim();
		if (v.probe_every_ms.trim()) d.health_probe.every_ms = Number(v.probe_every_ms);
	}
	return Object.keys(d).length ? d : undefined;
}

function webhook(v: FormValues): WebhookVerification | null {
	if (!v.webhook_scheme) return null;
	const w: WebhookVerification = { scheme: v.webhook_scheme as WebhookScheme };
	if (v.webhook_header.trim()) w.header = v.webhook_header.trim();
	if (v.webhook_max_skew_ms.trim()) w.max_skew_ms = Number(v.webhook_max_skew_ms);
	const ips = ipList(v);
	if (ips.length) w.ip_allowlist = ips;
	return w;
}

export type Built<T> = { ok: true; body: T } | { ok: false; fields: FieldErrors };

export function createBody(v: FormValues, reason: string): Built<VendorCreate> {
	const found = problems(v, 'create');
	if (Object.keys(found).length) return { ok: false, fields: found };
	return { ok: true, body: { vendor_id: v.vendor_id.trim(), ...fields(v), reason } };
}

/**
 * The PUT body. `before` is the vendor as the page loaded it: an edit that changes nothing is a
 * form problem, not a new revision.
 */
export function updateBody(v: FormValues, before: FormValues, reason: string): Built<VendorUpdate> {
	const found = problems(v, 'edit');
	if (Object.keys(found).length) return { ok: false, fields: found };
	if (!hasChanges(v, before))
		return { ok: false, fields: { '/display_name': 'Nothing changed. Edit at least one field.' } };
	return { ok: true, body: { ...fields(v), reason } };
}

/** True when the form would send something different from `before`. */
export function hasChanges(v: FormValues, before: FormValues): boolean {
	return JSON.stringify(fields(v)) !== JSON.stringify(fields(before));
}

/** "Window (ms) 30,000; Minimum calls 20; the rest Hub defaults" for the vendor page. */
export function breakerText(b: BreakerConfig | undefined): string {
	const set = BREAKER_FIELDS.filter((f) => b?.[f.key] != null).map(
		(f) => `${f.label} ${(b![f.key] as number).toLocaleString('en-US')}`
	);
	if (!set.length) return 'Hub defaults';
	return set.length === BREAKER_FIELDS.length
		? set.join('; ')
		: `${set.join('; ')}; the rest Hub defaults`;
}

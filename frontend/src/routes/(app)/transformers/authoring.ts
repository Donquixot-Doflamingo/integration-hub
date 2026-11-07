import type { FieldErrors } from '$lib/api/actions';
import type {
	GoldenFixture,
	TransformerDraft,
	TransformerKind,
	TransformerVersion,
	TransformerVersionCreate
} from '$lib/api/types';
import { parseJsonObject, prettyJson } from '$lib/utils/json-input';

/**
 * Transformer authoring (HUB-293): a new transformer (version 1) or the next version of one, from
 * the YAML source and its golden fixtures, checked against `TransformerVersionCreate`. The Hub
 * refuses a version whose goldens fail (422 `PUBLISH_VALIDATION_FAILED`, AGENTS §3.4), so the
 * console asks for a passing goldens run of the exact draft before it offers "Create". Errors are
 * keyed by JSON pointer into the create body (`/yaml`, `/fixtures/0/input`).
 */
export const TRANSFORMER_ID = /^[A-Za-z][A-Za-z0-9_-]{0,63}$/;
export const CAPABILITY = /^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+@v[1-9][0-9]*$/;
export const VENDOR_ENDPOINT = /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}\.[a-z][a-z0-9_]{0,63}@[1-9][0-9]*$/;
export const FIXTURE_NAME = /^[a-z0-9][a-z0-9_-]{0,63}$/;
export const KINDS: readonly TransformerKind[] = ['request', 'response', 'inbound'];

/** Golden fixture fields that hold JSON objects. */
export type FixtureField =
	| 'input'
	| 'account_config'
	| 'expected_request'
	| 'vendor_response'
	| 'expected_response'
	| 'expected_error';

export const fieldLabels: Record<FixtureField, string> = {
	input: 'Canonical input',
	account_config: 'Account config (optional)',
	expected_request: 'Expected vendor request',
	vendor_response: 'Vendor response',
	expected_response: 'Expected canonical response',
	expected_error: 'Expected canonical error'
};

/** The fields a fixture of `kind` uses (GoldenFixture description: request vs response). */
export function fieldsFor(kind: string, outcome: 'response' | 'error'): FixtureField[] {
	if (kind === 'response')
		return [
			'vendor_response',
			outcome === 'error' ? 'expected_error' : 'expected_response',
			'account_config'
		];
	return ['input', 'expected_request', 'account_config'];
}

export interface FixtureRow {
	name: string;
	/** JSON text per field. */
	json: Partial<Record<FixtureField, string>>;
	/** Response transformers: the fixture expects a canonical response or a canonical error. */
	outcome: 'response' | 'error';
}

export interface FormValues {
	transformer_id: string;
	kind: string;
	capability: string;
	vendor_endpoint: string;
	docs_url: string;
	yaml: string;
	fixtures: FixtureRow[];
}

export function emptyFixture(n = 1): FixtureRow {
	return { name: `case_${n}`, json: {}, outcome: 'response' };
}

export function emptyValues(): FormValues {
	return {
		transformer_id: '',
		kind: 'request',
		capability: '',
		vendor_endpoint: '',
		docs_url: '',
		yaml: '',
		fixtures: [emptyFixture()]
	};
}

const FIELDS: FixtureField[] = [
	'input',
	'account_config',
	'expected_request',
	'vendor_response',
	'expected_response',
	'expected_error'
];

/** The next version starts from the latest one: same kind, capability, endpoint, source, fixtures. */
export function valuesFromVersion(v: TransformerVersion): FormValues {
	return {
		transformer_id: v.transformer_id,
		kind: v.kind,
		capability: v.capability,
		vendor_endpoint: v.vendor_endpoint,
		docs_url: v.docs_url ?? '',
		yaml: v.yaml,
		fixtures: v.fixtures.map((f) => ({
			name: f.name,
			outcome: f.expected_error !== undefined ? 'error' : 'response',
			json: Object.fromEntries(
				FIELDS.filter((k) => f[k] !== undefined).map((k) => [k, prettyJson(f[k])])
			)
		}))
	};
}

/** The fixtures as the API takes them, or problems keyed by `/fixtures/<i>[/<field>]`. */
export function buildFixtures(
	rows: readonly FixtureRow[],
	kind: string
): { ok: true; fixtures: GoldenFixture[] } | { ok: false; fields: FieldErrors } {
	const fields: FieldErrors = {};
	const out: GoldenFixture[] = [];
	if (!rows.length) fields['/fixtures'] = 'Add at least one golden fixture.';
	const seen = new Set<string>();
	rows.forEach((r, i) => {
		const at = `/fixtures/${i}`;
		const name = r.name.trim();
		if (!FIXTURE_NAME.test(name))
			fields[`${at}/name`] = 'Lower-case letters, digits, "_" or "-" (e.g. order_placed).';
		else if (seen.has(name)) fields[`${at}/name`] = `"${name}" is used twice.`;
		seen.add(name);
		const f: GoldenFixture = { name };
		for (const key of fieldsFor(kind, r.outcome)) {
			const parsed = parseJsonObject(r.json[key] ?? '', {
				optional: key === 'account_config',
				what: fieldLabels[key].replace(' (optional)', '')
			});
			if (!parsed.ok) fields[`${at}/${key}`] = parsed.message;
			else if (parsed.value) f[key] = parsed.value;
		}
		out.push(f);
	});
	return Object.keys(fields).length ? { ok: false, fields } : { ok: true, fixtures: out };
}

export function draftOf(v: FormValues): TransformerDraft {
	return {
		kind: v.kind as TransformerKind,
		capability: v.capability.trim(),
		vendor_endpoint: v.vendor_endpoint.trim(),
		yaml: v.yaml
	};
}

/** Problems with the draft itself (what goldens and dry runs need). */
export function draftProblems(v: FormValues): FieldErrors {
	const out: FieldErrors = {};
	if (!(KINDS as readonly string[]).includes(v.kind)) out['/kind'] = 'Pick request or response.';
	if (!CAPABILITY.test(v.capability.trim())) out['/capability'] = 'Pick the capability.';
	if (!VENDOR_ENDPOINT.test(v.vendor_endpoint.trim()))
		out['/vendor_endpoint'] = 'A vendor endpoint version such as clevertap.upload@1.';
	if (!v.yaml.trim()) out['/yaml'] = 'Write the transformer source (YAML).';
	return out;
}

export function problems(v: FormValues, mode: 'new' | 'version'): FieldErrors {
	const out = draftProblems(v);
	if (mode === 'new' && !TRANSFORMER_ID.test(v.transformer_id.trim()))
		out['/transformer_id'] = 'Letters, digits, "_" or "-", starting with a letter (e.g. T3).';
	if (v.docs_url.trim()) {
		try {
			const u = new URL(v.docs_url.trim());
			if (u.protocol !== 'https:' && u.protocol !== 'http:') throw new Error('scheme');
		} catch {
			out['/docs_url'] = 'An absolute http(s) URL of the vendor documentation.';
		}
	}
	const fx = buildFixtures(v.fixtures, v.kind);
	if (!fx.ok) Object.assign(out, fx.fields);
	return out;
}

/**
 * Identity of what a goldens run checked: kind, capability, endpoint, source and fixtures. A
 * passing run unlocks "Create" only while the draft is still exactly this.
 */
export function draftKey(v: FormValues): string {
	const fx = buildFixtures(v.fixtures, v.kind);
	return JSON.stringify([draftOf(v), fx.ok ? fx.fixtures : null]);
}

export function createBody(
	v: FormValues,
	mode: 'new' | 'version',
	expectedLatest: number | null,
	reason: string
): { ok: true; body: TransformerVersionCreate } | { ok: false; fields: FieldErrors } {
	const found = problems(v, mode);
	const fx = buildFixtures(v.fixtures, v.kind);
	if (Object.keys(found).length || !fx.ok) return { ok: false, fields: found };
	return {
		ok: true,
		body: {
			...draftOf(v),
			expected_latest_version: mode === 'new' ? null : expectedLatest,
			fixtures: fx.fixtures,
			docs_url: v.docs_url.trim() || null,
			reason
		}
	};
}

/** Fixture rows posted as one JSON field (the page builds them; the action re-checks them). */
export function rowsFromJson(raw: string): FixtureRow[] | null {
	try {
		const v = JSON.parse(raw) as unknown;
		if (!Array.isArray(v)) return null;
		return v.map((r) => ({
			name: String((r as FixtureRow).name ?? ''),
			outcome: (r as FixtureRow).outcome === 'error' ? 'error' : 'response',
			json: Object.fromEntries(
				Object.entries((r as FixtureRow).json ?? {}).filter(
					(e): e is [FixtureField, string] =>
						(FIELDS as string[]).includes(e[0]) && typeof e[1] === 'string'
				)
			)
		}));
	} catch {
		return null;
	}
}

export function valuesFromForm(form: FormData): FormValues | null {
	const get = (n: string) => {
		const v = form.get(n);
		return typeof v === 'string' ? v : '';
	};
	const fixtures = rowsFromJson(get('fixtures'));
	if (!fixtures) return null;
	return {
		transformer_id: get('transformer_id'),
		kind: get('kind'),
		capability: get('capability'),
		vendor_endpoint: get('vendor_endpoint'),
		docs_url: get('docs_url'),
		// Form submission turns textarea line breaks into CRLF; the source is stored as typed.
		yaml: get('yaml').replace(/\r\n/g, '\n'),
		fixtures
	};
}

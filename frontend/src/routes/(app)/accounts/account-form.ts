import type { FieldErrors } from '$lib/api/actions';
import type {
	Account,
	AccountCreate,
	AccountQuota,
	AccountUpdate,
	CredentialSlot
} from '$lib/api/types';
import { parseJsonObject } from '$lib/utils/json-input';
import { normalizeTime } from '$lib/utils/time';

/**
 * Account create / edit and credential forms (HUB-291), checked against `AccountCreate`,
 * `AccountUpdate` and `CredentialAddNext` before anything is sent. Errors are keyed by the JSON
 * pointer of the body property, like the API's `VALIDATION_FAILED` details.
 *
 * Credentials are write-only (contract `Credentials`, `writeOnly: true`): the console takes the
 * values in password inputs, posts them to its own server action and forgets them as soon as the
 * browser captured the form (whatever the outcome). They are never rendered, logged, stored, put
 * in a URL or sent back to the browser (the action scrubs them from any error text).
 */
export const SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/;
/** Credential keys are what transformers read as `creds.<key>`. */
export const CRED_KEY = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/;

/** One credential: its key (shown) and its value (never shown again). */
export interface CredentialRow {
	key: string;
	value: string;
}

export const CRED_NAMES = { key: 'cred_key', value: 'cred_value' } as const;

export interface FormValues {
	account_id: string;
	vendor_id: string;
	region: string;
	/** JSON object text. */
	config: string;
	rps: string;
	daily: string;
	credentials: CredentialRow[];
	/** `datetime-local`, read as UTC. */
	credential_expires_at: string;
	/** The vendor's auth scheme; `none` (HUB-436): the account has no credentials. */
	auth_scheme: string;
}

/** Accounts of an auth `none` vendor carry no credentials (contract `AccountCreate`). */
export const needsCredentials = (v: Pick<FormValues, 'auth_scheme'>) => v.auth_scheme !== 'none';

export function emptyValues(): FormValues {
	return {
		account_id: '',
		vendor_id: '',
		region: '',
		config: '{}',
		rps: '',
		daily: '',
		credentials: [{ key: '', value: '' }],
		credential_expires_at: '',
		auth_scheme: ''
	};
}

const get = (form: FormData, name: string) => {
	const v = form.get(name);
	return typeof v === 'string' ? v : '';
};

/** The credential rows of a posted form (keys and values pair up by position). */
export function credentialsFromForm(form: FormData): CredentialRow[] {
	const keys = form.getAll(CRED_NAMES.key).map(String);
	const values = form.getAll(CRED_NAMES.value).map(String);
	return keys.map((key, i) => ({ key, value: values[i] ?? '' }));
}

export function valuesFromForm(form: FormData): FormValues {
	return {
		account_id: get(form, 'account_id'),
		vendor_id: get(form, 'vendor_id'),
		region: get(form, 'region'),
		config: get(form, 'config'),
		rps: get(form, 'rps'),
		daily: get(form, 'daily'),
		credentials: credentialsFromForm(form),
		credential_expires_at: get(form, 'credential_expires_at'),
		auth_scheme: get(form, 'auth_scheme')
	};
}

export const CREATE_POINTERS = [
	'/account_id',
	'/vendor_id',
	'/region',
	'/config',
	'/quota',
	'/quota/rps',
	'/quota/daily',
	'/credentials',
	'/credential_expires_at'
] as const;

const NUM = /^\d+(\.\d+)?$/;
const INT = /^\d+$/;

/** Problems with the credential rows (at least one; keys unique and well-formed; values set). */
export function credentialProblem(rows: readonly CredentialRow[]): string | null {
	const used = rows.filter((r) => r.key.trim() || r.value);
	if (!used.length) return 'Add at least one credential (a key and its secret value).';
	const seen = new Set<string>();
	for (const r of used) {
		const key = r.key.trim();
		if (!key) return 'Every secret value needs a key, e.g. passcode.';
		if (!CRED_KEY.test(key))
			return `"${key}": letters, digits and "_", starting with a letter or "_".`;
		if (seen.has(key)) return `"${key}" is listed twice.`;
		if (!r.value) return `Enter the value of "${key}".`;
		seen.add(key);
	}
	return null;
}

function quotaProblems(v: Pick<FormValues, 'rps' | 'daily'>, out: FieldErrors) {
	if (v.rps.trim() && !NUM.test(v.rps.trim()))
		out['/quota/rps'] = 'A number of requests per second, 0 or more.';
	if (v.daily.trim() && !INT.test(v.daily.trim()))
		out['/quota/daily'] = 'A whole number of requests per day, 0 or more.';
}

/** The JSON object in `text`, or `{}` (call after the problems were checked). */
function objectOf(text: string): Record<string, unknown> {
	const r = parseJsonObject(text);
	return r.ok ? (r.value ?? {}) : {};
}

function configProblem(text: string): string | null {
	const r = parseJsonObject(text, { what: 'The config' });
	return r.ok ? null : r.message;
}

export function problems(v: FormValues, now = Date.now()): FieldErrors {
	const out: FieldErrors = {};
	const id = v.account_id.trim();
	if (!id) out['/account_id'] = 'Enter an account id.';
	else if (!SLUG.test(id))
		out['/account_id'] =
			'3 to 64 characters: letters, digits, "_" or "-", starting with a letter or digit.';
	if (!v.vendor_id) out['/vendor_id'] = 'Pick the vendor.';
	if (!v.region) out['/region'] = "Pick one of the vendor's regions.";
	const config = configProblem(v.config);
	if (config) out['/config'] = config;
	quotaProblems(v, out);
	if (needsCredentials(v)) {
		const creds = credentialProblem(v.credentials);
		if (creds) out['/credentials'] = creds;
		const exp = expiryProblem(v.credential_expires_at, now);
		if (exp) out['/credential_expires_at'] = exp;
	}
	return out;
}

export function expiryProblem(raw: string, now = Date.now()): string | null {
	if (!raw.trim()) return null;
	const at = normalizeTime(raw);
	if (!at) return 'Enter a date and time (UTC).';
	if (Date.parse(at) <= now) return 'Pick a time in the future.';
	return null;
}

function credentials(rows: readonly CredentialRow[]): Record<string, string> {
	const out: Record<string, string> = {};
	for (const r of rows) if (r.key.trim()) out[r.key.trim()] = r.value;
	return out;
}

function quota(v: Pick<FormValues, 'rps' | 'daily'>): AccountQuota | undefined {
	const q: AccountQuota = {};
	if (v.rps.trim()) q.rps = Number(v.rps);
	if (v.daily.trim()) q.daily = Number(v.daily);
	return Object.keys(q).length ? q : undefined;
}

export type Built<T> = { ok: true; body: T } | { ok: false; fields: FieldErrors };

export function createBody(v: FormValues, reason: string, now = Date.now()): Built<AccountCreate> {
	const found = problems(v, now);
	if (Object.keys(found).length) return { ok: false, fields: found };
	const body: AccountCreate = {
		account_id: v.account_id.trim(),
		vendor_id: v.vendor_id,
		region: v.region,
		config: objectOf(v.config),
		reason
	};
	if (needsCredentials(v)) {
		body.credentials = credentials(v.credentials);
		body.credential_expires_at = v.credential_expires_at.trim()
			? normalizeTime(v.credential_expires_at)
			: null;
	}
	const q = quota(v);
	if (q) body.quota = q;
	return { ok: true, body };
}

/** The secret values of the rows, to scrub from anything that goes back to the browser. */
export function secretValues(rows: readonly CredentialRow[]): string[] {
	return rows.map((r) => r.value).filter((s) => s.length > 0);
}

// ---- config and quota edit (updateAccount, JSON merge-patch) ---------------------------------

export interface EditValues {
	config: string;
	rps: string;
	daily: string;
}

export const EDIT_POINTERS = ['/config', '/quota', '/quota/rps', '/quota/daily'] as const;

export function editValuesFromAccount(a: Account): EditValues {
	return {
		config: JSON.stringify(a.config ?? {}, null, 2),
		rps: a.quota.rps == null ? '' : String(a.quota.rps),
		daily: a.quota.daily == null ? '' : String(a.quota.daily)
	};
}

export function editProblems(v: EditValues): FieldErrors {
	const out: FieldErrors = {};
	const config = configProblem(v.config);
	if (config) out['/config'] = config;
	quotaProblems(v, out);
	return out;
}

/**
 * JSON merge-patch (RFC 7396) that turns `before` into `after`: changed or new keys carry their
 * new value, removed keys are `null`, nested objects recurse. Empty when nothing changed.
 */
export function mergePatch(
	before: Record<string, unknown>,
	after: Record<string, unknown>
): Record<string, unknown> {
	const out: Record<string, unknown> = {};
	for (const k of Object.keys(before)) if (!(k in after)) out[k] = null;
	for (const [k, v] of Object.entries(after)) {
		const old = before[k];
		if (isPlainObject(v) && isPlainObject(old)) {
			const sub = mergePatch(old, v);
			if (Object.keys(sub).length) out[k] = sub;
		} else if (JSON.stringify(v) !== JSON.stringify(old)) {
			out[k] = v;
		}
	}
	return out;
}

const isPlainObject = (v: unknown): v is Record<string, unknown> =>
	typeof v === 'object' && v !== null && !Array.isArray(v);

/** Only what changed: a config merge-patch and the changed quota fields. */
export function updateBody(
	v: EditValues,
	before: EditValues,
	reason: string
): Built<AccountUpdate> {
	const found = editProblems(v);
	if (Object.keys(found).length) return { ok: false, fields: found };
	const body: AccountUpdate = { reason };
	const patch = mergePatch(objectOf(before.config), objectOf(v.config));
	if (Object.keys(patch).length) body.config = patch;
	const q: AccountQuota = {};
	if (v.rps.trim() && Number(v.rps) !== Number(before.rps || NaN)) q.rps = Number(v.rps);
	// `rps` is a number in the contract (never null): an account with a rate keeps one (HUB-455).
	else if (!v.rps.trim() && before.rps.trim())
		return {
			ok: false,
			fields: { '/quota/rps': 'The rate cannot be removed. Enter the new requests per second.' }
		};
	if (v.daily.trim() && Number(v.daily) !== Number(before.daily || NaN)) q.daily = Number(v.daily);
	// `daily` is `integer | null` (JSON merge-patch): an emptied field clears the daily quota.
	else if (!v.daily.trim() && before.daily.trim()) q.daily = null;
	if (Object.keys(q).length) body.quota = q;
	if (Object.keys(body).length === 1)
		return { ok: false, fields: { '/config': 'Nothing changed. Edit the config or the quota.' } };
	return { ok: true, body };
}

export function hasEditChanges(v: EditValues, before: EditValues): boolean {
	return updateBody(v, before, 'x').ok;
}

// ---- credential slots -------------------------------------------------------------------------

export function slot(
	slots: readonly CredentialSlot[],
	name: CredentialSlot['slot']
): CredentialSlot | undefined {
	return slots.find((s) => s.slot === name);
}

export const slotLabels: Record<CredentialSlot['slot'], string> = {
	active: 'Active',
	next: 'Next (not used yet)',
	previous: 'Previous (kept, not used)'
};

/** Where the rotation stands, for the stepper: which step is next. */
export type RotationStep = 'add' | 'test' | 'promote' | 'delete' | 'done';

export function rotationStep(
	slots: readonly CredentialSlot[],
	testedNext: number | null
): RotationStep {
	const next = slot(slots, 'next');
	if (next) return testedNext === next.cred_version ? 'promote' : 'test';
	if (slot(slots, 'previous')) return 'delete';
	return 'add';
}

import type { FieldErrors } from '$lib/api/actions';
import type { ApiKey, ApiKeyCreate, ApiKeyScope, ApiKeyState } from '$lib/api/types';
import { formatDurationMs, formatUtc } from '$lib/utils/format';
import { normalizeTime } from '$lib/utils/time';

/**
 * API keys of a tenant (HUB-271), against `ApiKeyCreate` / `ApiKeyRotate` in hub-admin.v1.yaml.
 * Scopes are `invoke`, `dry_run` and `requests_read`, each optionally narrowed to one integration
 * as `<scope>:<integration_id>` (`ApiKeyScope` pattern).
 */
export const BASE_SCOPES = ['invoke', 'dry_run', 'requests_read'] as const;
export type BaseScope = (typeof BASE_SCOPES)[number];

export const scopeLabels: Record<BaseScope, string> = {
	invoke: 'invoke: call integrations',
	dry_run: 'dry_run: validate and preview calls without sending them',
	requests_read: 'requests_read: read its own requests'
};

export const STATES: readonly ApiKeyState[] = ['active', 'expiring', 'revoked', 'expired'];

export const stateLabels: Record<ApiKeyState, string> = {
	active: 'Active',
	expiring: 'Expiring',
	revoked: 'Revoked',
	expired: 'Expired'
};

/** The integration part of a narrowed scope (contract: `[A-Za-z0-9][A-Za-z0-9_-]{2,63}`). */
const INTEGRATION = /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/;
export const SCOPE = /^(invoke|dry_run|requests_read)(:[A-Za-z0-9][A-Za-z0-9_-]{2,63})?$/;

/** Rotation overlaps offered (contract `overlap_ms`: 0 to 30 days). Default: 24 h. */
export const OVERLAPS = [
	{ ms: 0, label: 'None: the old key stops at once' },
	{ ms: 3_600_000, label: '1 hour' },
	{ ms: 86_400_000, label: '24 hours' },
	{ ms: 7 * 86_400_000, label: '7 days' },
	{ ms: 30 * 86_400_000, label: '30 days (maximum)' }
] as const;
export const DEFAULT_OVERLAP_MS = 86_400_000;
export const MAX_OVERLAP_MS = 2_592_000_000;

export interface KeyFormValues {
	scopes: string[];
	integration: string;
	description: string;
	/** `datetime-local` value, read as UTC. */
	expires: string;
}

export function keyValuesFromForm(form: FormData): KeyFormValues {
	const str = (k: string) => {
		const v = form.get(k);
		return typeof v === 'string' ? v : '';
	};
	return {
		scopes: form.getAll('scope').filter((s): s is string => typeof s === 'string'),
		integration: str('integration'),
		description: str('description'),
		expires: str('expires_at')
	};
}

export function keyProblems(v: KeyFormValues, now = Date.now()): FieldErrors {
	const out: FieldErrors = {};
	const scopes = v.scopes.filter((s) => (BASE_SCOPES as readonly string[]).includes(s));
	if (scopes.length === 0 || scopes.length !== v.scopes.length)
		out.scopes = 'Pick at least one scope.';
	const integration = v.integration.trim();
	if (integration && !INTEGRATION.test(integration))
		out.integration = 'An integration id: 3 to 64 letters, digits, "_" or "-".';
	if (v.expires.trim()) {
		const at = normalizeTime(v.expires);
		if (!at) out.expires_at = 'Enter a date and time (UTC).';
		else if (Date.parse(at) <= now) out.expires_at = 'Pick a time in the future.';
	}
	return out;
}

export function createKeyBody(
	tenantId: string,
	v: KeyFormValues,
	reason: string,
	now = Date.now()
): { ok: true; body: ApiKeyCreate } | { ok: false; fields: FieldErrors } {
	const fields = keyProblems(v, now);
	if (Object.keys(fields).length) return { ok: false, fields };
	const integration = v.integration.trim();
	const scopes = BASE_SCOPES.filter((s) => v.scopes.includes(s)).map(
		(s) => (integration ? `${s}:${integration}` : s) as ApiKeyScope
	);
	return {
		ok: true,
		body: {
			tenant_id: tenantId,
			description: v.description.trim() || null,
			scopes,
			expires_at: v.expires.trim() ? normalizeTime(v.expires) : null,
			reason
		}
	};
}

/** `overlap_ms` from the rotate form, or null when it is not an allowed value. */
export function overlapFromForm(form: FormData): number | null {
	const raw = String(form.get('overlap_ms') ?? '');
	if (!/^\d+$/.test(raw)) return null;
	const ms = Number(raw);
	return ms <= MAX_OVERLAP_MS ? ms : null;
}

/** Key ids are `key_` + ULID (contract `KeyIdValue`). */
export const KEY_ID = /^key_[0-9A-HJKMNP-TV-Z]{26}$/;

/** What the reveal says about the replaced key after a rotation. */
export function overlapNote(old: ApiKey, overlapMs: number): string {
	if (old.state === 'revoked' || overlapMs === 0) {
		return `The old key ${old.prefix} no longer works. Switch callers to the new key now.`;
	}
	return `The old key ${old.prefix} keeps working until ${formatUtc(old.expires_at)} (overlap ${formatDurationMs(overlapMs)}), then it is revoked automatically. Switch callers to the new key before then.`;
}

/** True for keys that still authenticate (and can be revoked). */
export function isLive(k: ApiKey): boolean {
	return k.state === 'active' || k.state === 'expiring';
}

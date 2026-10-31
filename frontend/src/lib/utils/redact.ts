/**
 * Defence in depth for payload views (HUB-264): the admin API already applies the audit policy
 * (masked values are hashed, secrets are redacted in `request.transformed`), and the console never
 * tries to unmask anything. This only hides values under secret-looking keys that are not already
 * masked, in case a payload slips through unmasked. It never reveals more than the API returned.
 */
const SECRET_KEY =
	/^(authorization|proxy-authorization|cookie|set-cookie|x-api-key|api[-_]?key|apikey|x-hub-key|password|passwd|secret|client[-_]?secret|token|access[-_]?token|refresh[-_]?token|id[-_]?token|private[-_]?key|signature|x-signature)$/i;
const ALREADY_MASKED = /^(sha256:|hmac:|\*{3}|\[redacted|<redacted)/i;

export const REDACTED = '[redacted by console]';

export function isSecretKey(key: string): boolean {
	return SECRET_KEY.test(key);
}

/** A deep copy of `value` with unmasked secret-looking fields replaced by `REDACTED`. */
export function redactSecrets(value: unknown, depth = 0): unknown {
	if (depth > 64) return REDACTED;
	if (Array.isArray(value)) return value.map((v) => redactSecrets(v, depth + 1));
	if (value === null || typeof value !== 'object') return value;
	const out: Record<string, unknown> = {};
	for (const [k, v] of Object.entries(value)) {
		if (
			isSecretKey(k) &&
			v !== null &&
			v !== '' &&
			!(typeof v === 'string' && ALREADY_MASKED.test(v))
		) {
			out[k] = REDACTED;
		} else {
			out[k] = redactSecrets(v, depth + 1);
		}
	}
	return out;
}

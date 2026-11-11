import { createCipheriv, createDecipheriv, hkdfSync, randomBytes } from 'node:crypto';

/**
 * AES-256-GCM sealing for the cookies the console sets (HUB-492, AUTH_PLAN G14). The key comes
 * from `HUB_SESSION_SECRET` through HKDF with a purpose label, so a value sealed for one cookie
 * (the login state) can never be opened as another (the session), and the access token is not
 * readable from the cookie value. Layout: `base64url(iv[12] | tag[16] | ciphertext)`; the purpose
 * is also the GCM additional data. Tampering, a wrong secret or a wrong purpose all give null.
 */
export type SealPurpose = 'session' | 'login';

const IV_BYTES = 12;
const TAG_BYTES = 16;

function key(secret: string, purpose: SealPurpose): Buffer {
	return Buffer.from(hkdfSync('sha256', secret, 'hub-console', `cookie:${purpose}`, 32));
}

export function seal(payload: unknown, secret: string, purpose: SealPurpose): string {
	const iv = randomBytes(IV_BYTES);
	const cipher = createCipheriv('aes-256-gcm', key(secret, purpose), iv);
	cipher.setAAD(Buffer.from(purpose));
	const body = Buffer.concat([cipher.update(JSON.stringify(payload), 'utf8'), cipher.final()]);
	return Buffer.concat([iv, cipher.getAuthTag(), body]).toString('base64url');
}

/** The sealed payload, or null when the value is missing, malformed, tampered or for another purpose. */
export function open(
	value: string | undefined,
	secret: string,
	purpose: SealPurpose
): unknown | null {
	if (!value) return null;
	try {
		const raw = Buffer.from(value, 'base64url');
		if (raw.length <= IV_BYTES + TAG_BYTES) return null;
		const decipher = createDecipheriv(
			'aes-256-gcm',
			key(secret, purpose),
			raw.subarray(0, IV_BYTES)
		);
		decipher.setAAD(Buffer.from(purpose));
		decipher.setAuthTag(raw.subarray(IV_BYTES, IV_BYTES + TAG_BYTES));
		const text = Buffer.concat([
			decipher.update(raw.subarray(IV_BYTES + TAG_BYTES)),
			decipher.final()
		]).toString('utf8');
		return JSON.parse(text);
	} catch {
		return null;
	}
}

import { DEVICE_COOKIE, DEVICE_TTL_SECONDS, isDeviceId, newDeviceId } from './login-flow';
import type { CookieJar } from './cookie';

/**
 * The browser's stable device id (`hub_device`): created on first sign-in, kept for a year.
 * ISFC binds the session to it (`x-device-id`), so a refresh from a different device is refused.
 * It is not a secret and not an authenticator; it is httpOnly anyway.
 */
export function deviceIdOf(cookies: CookieJar, secure: boolean): string {
	const existing = cookies.get(DEVICE_COOKIE);
	if (isDeviceId(existing)) return existing;
	const id = newDeviceId();
	cookies.set(DEVICE_COOKIE, id, {
		path: '/',
		httpOnly: true,
		sameSite: 'lax',
		secure,
		maxAge: DEVICE_TTL_SECONDS
	});
	return id;
}

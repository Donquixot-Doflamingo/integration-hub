/**
 * Messages for the ISFC error codes the console shows (AUTH_PLAN §9, "Errors"). ISFC answers
 * `{ error_code, error, message }`; the console never shows ISFC's raw text, only these. The login
 * page gets the code in `?error=` and looks the message up here, so nothing else can be injected.
 */
export const ISFC_ERROR_MESSAGES: Record<string, string> = {
	// auth-service
	AS_FB_21:
		'You have no tenant access in the Integration Hub console. Ask a platform admin to add you to a tenant in ISFC.',
	AS_VE_34: 'Your email domain is not allowed to sign in to the Integration Hub console.',
	// api-gateway
	GW_007: 'Your ISFC session has ended. Sign in again.',
	GW_015:
		'You have no access to that tenant in the Integration Hub. Pick another tenant, or ask for access in ISFC.',
	// the console's own
	state_mismatch:
		'The sign-in could not be verified (the login state did not match). Start the sign-in again.',
	login_expired: 'The sign-in took too long or was started in another browser. Start it again.',
	access_denied: 'Sign-in was cancelled or refused by the identity provider.',
	unavailable: 'ISFC did not answer. Try again in a moment.',
	sign_in_failed: 'Sign-in failed. Try again, or ask a platform admin if it keeps happening.'
};

/** The message for a code, or null when the code is not one the console knows. */
export function isfcErrorMessage(code: string | null | undefined): string | null {
	return code && Object.hasOwn(ISFC_ERROR_MESSAGES, code) ? ISFC_ERROR_MESSAGES[code]! : null;
}

/** A code safe to put in a URL: a known one, or the generic failure. */
export function knownErrorCode(code: string | null | undefined): string {
	return code && Object.hasOwn(ISFC_ERROR_MESSAGES, code) ? code : 'sign_in_failed';
}

export function isKnownErrorCode(code: string | null | undefined): code is string {
	return !!code && Object.hasOwn(ISFC_ERROR_MESSAGES, code);
}

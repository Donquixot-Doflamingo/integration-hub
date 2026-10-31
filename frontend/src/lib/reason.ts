/**
 * The change reason every mutation sends (contract: `Reason`, recorded in change_log).
 * The contract allows 3..1024 characters; the console asks for at least 10 so the change log stays
 * useful to the next on-call (admin-console-ux §1). `unsealRequest` itself requires 10.
 */
export const REASON_MIN = 10;
export const REASON_MAX = 1024;

/** Why `reason` is not acceptable yet, or null when it is. Input is trimmed first. */
export function reasonProblem(reason: string): string | null {
	const r = reason.trim();
	if (r.length === 0) return 'Enter a reason. It is saved in the change log.';
	if (r.length < REASON_MIN) return `Use at least ${REASON_MIN} characters (${r.length} so far).`;
	if (r.length > REASON_MAX) return `Use at most ${REASON_MAX} characters.`;
	return null;
}

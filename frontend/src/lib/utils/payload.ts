/**
 * How a stored payload may be shown (DESIGN §13.2): sealed (locked placeholder), metadata only
 * (no body kept), none (nothing returned) or shown as the API returned it (masked stays masked).
 * A shown value is any JSON: a pass-through result is the vendor's body as it was (ADR-0023).
 * Used by the request timeline (HUB-264) and the DLQ entry (HUB-266).
 */
export type PayloadView =
	| { kind: 'sealed' }
	| { kind: 'metadata_only' }
	| { kind: 'none' }
	| { kind: 'shown'; value: unknown };

/** A payload object the API returned, or null: shown when present, `none` otherwise. */
export function shownOrNone(value: Record<string, unknown> | null | undefined): PayloadView {
	return value ? { kind: 'shown', value } : { kind: 'none' };
}

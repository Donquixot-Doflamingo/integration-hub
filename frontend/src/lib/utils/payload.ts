/**
 * How a stored payload may be shown (DESIGN §13.2): sealed (locked placeholder), metadata only
 * (no body kept), none (nothing returned) or shown as the API returned it (masked stays masked).
 * Used by the request timeline (HUB-264) and the DLQ entry (HUB-266).
 */
export type PayloadView =
	| { kind: 'sealed' }
	| { kind: 'metadata_only' }
	| { kind: 'none' }
	| { kind: 'shown'; value: Record<string, unknown> };

/** A payload object the API returned, or null: shown when present, `none` otherwise. */
export function shownOrNone(value: Record<string, unknown> | null | undefined): PayloadView {
	return value ? { kind: 'shown', value } : { kind: 'none' };
}

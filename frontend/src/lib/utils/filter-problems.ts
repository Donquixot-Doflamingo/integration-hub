/**
 * Filter forms (requests, DLQ, change log) parse what was typed with the same rules as the URL,
 * which drops a value that does not fit. Dropping it silently showed the unfiltered list while the
 * input kept the text (HUB-455); these helpers name the fields that were dropped so the page can
 * mark them and not run the search.
 */
export type FilterProblems<K extends string = string> = Partial<Record<K, string>>;

/** Slug-like ids (tenant, integration, vendor, account): what the contract's `Slug` allows. */
export const SLUG_PROBLEM =
	'Use 3 to 64 letters, digits, "-" or "_", starting with a letter or digit.';

/**
 * The fields whose submitted text did not survive parsing. `raw` holds the trimmed submitted text
 * per field; a list field (`code`, comma-separated) is dropped when any of its entries was.
 */
export function droppedFields<K extends string>(
	raw: Partial<Record<K, string>>,
	kept: Partial<Record<K, unknown>>,
	messages: Record<K, string>
): FilterProblems<K> {
	const out: FilterProblems<K> = {};
	for (const key of Object.keys(raw) as K[]) {
		const text = raw[key];
		if (!text) continue;
		const k = kept[key];
		const ok = Array.isArray(k)
			? k.length === new Set(text.split(',').filter(Boolean)).size
			: k !== undefined && k !== null && k !== '';
		if (!ok) out[key] = messages[key];
	}
	return out;
}

/**
 * Checks JSON typed by an operator before it is sent (transformer dry-run, HUB-269). Gives a
 * message with line and column so the problem can be found in a textarea. Runs in the browser
 * (submit is blocked) and again in the form action (no-JS path).
 */
export type JsonCheck =
	| { ok: true; value: Record<string, unknown> | undefined }
	| { ok: false; message: string; line: number | null; column: number | null };

/** 1-based line and column of a 0-based offset. */
export function lineColumn(text: string, offset: number): { line: number; column: number } {
	const before = text.slice(0, Math.max(0, Math.min(offset, text.length)));
	const lines = before.split('\n');
	return { line: lines.length, column: lines[lines.length - 1]!.length + 1 };
}

function where(text: string, e: unknown): { line: number | null; column: number | null } {
	const msg = e instanceof Error ? e.message : String(e);
	const lc = /line (\d+) column (\d+)/i.exec(msg);
	if (lc) return { line: Number(lc[1]), column: Number(lc[2]) };
	const pos = /position (\d+)/i.exec(msg);
	if (pos) return lineColumn(text, Number(pos[1]));
	if (/unexpected end/i.test(msg)) return lineColumn(text, text.length);
	return { line: null, column: null };
}

/**
 * `text` must be a JSON object. Empty text is `undefined` when `optional`, an error otherwise.
 * `stringValues` also requires every value to be a string (HTTP headers).
 */
export function parseJsonObject(
	text: string,
	opts: { optional?: boolean; stringValues?: boolean; what?: string } = {}
): JsonCheck {
	const what = opts.what ?? 'The value';
	if (!text.trim()) {
		return opts.optional
			? { ok: true, value: undefined }
			: { ok: false, message: `${what} is empty. Enter a JSON object.`, line: null, column: null };
	}
	let value: unknown;
	try {
		value = JSON.parse(text);
	} catch (e) {
		const at = where(text, e);
		const loc = at.line ? ` (line ${at.line}, column ${at.column})` : '';
		return { ok: false, message: `${what} is not valid JSON${loc}.`, ...at };
	}
	if (typeof value !== 'object' || value === null || Array.isArray(value)) {
		return {
			ok: false,
			message: `${what} must be a JSON object ({ ... }), not ${Array.isArray(value) ? 'an array' : value === null ? 'null' : `a ${typeof value}`}.`,
			line: 1,
			column: 1
		};
	}
	if (opts.stringValues) {
		const bad = Object.entries(value).find(([, v]) => typeof v !== 'string');
		if (bad) {
			return {
				ok: false,
				message: `${what}: "${bad[0]}" must be a string.`,
				line: null,
				column: null
			};
		}
	}
	return { ok: true, value: value as Record<string, unknown> };
}

/** Pretty JSON for a textarea. */
export function prettyJson(value: unknown): string {
	return value === undefined ? '' : JSON.stringify(value, null, 2);
}

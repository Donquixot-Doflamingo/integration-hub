/**
 * A small, safe YAML highlighter for read-only views (transformer DSL, HUB-268). It splits each
 * line into tokens; the view renders them as text in `<span>`s (never as HTML), so nothing in the
 * source can inject markup. It understands the subset the transformer files use: comments, keys,
 * list dashes, quoted strings, numbers, booleans/null, block scalar markers and `${...}`-style
 * expressions are left as plain values. Anything it does not recognise stays plain text.
 */
export type TokenKind = 'comment' | 'key' | 'punct' | 'string' | 'number' | 'literal' | 'plain';

export interface Token {
	kind: TokenKind;
	text: string;
}

const NUMBER = /^-?(0|[1-9]\d*)(\.\d+)?([eE][-+]?\d+)?$/;
const LITERAL = /^(true|false|null|~|yes|no|on|off)$/i;

/** Index of a ` #` comment start outside quotes, or -1. */
function commentStart(s: string): number {
	let quote: string | null = null;
	for (let i = 0; i < s.length; i++) {
		const c = s[i];
		if (quote) {
			if (c === '\\' && quote === '"') i++;
			else if (c === quote) quote = null;
		} else if (c === '"' || c === "'") quote = c;
		else if (c === '#' && (i === 0 || /\s/.test(s[i - 1]!))) return i;
	}
	return -1;
}

function valueTokens(v: string): Token[] {
	if (!v) return [];
	const lead = /^\s*/.exec(v)![0];
	const core = v.slice(lead.length).replace(/\s+$/, '');
	const trail = v.slice(lead.length + core.length);
	const out: Token[] = [];
	if (lead) out.push({ kind: 'plain', text: lead });
	if (core) {
		let kind: TokenKind = 'plain';
		if (/^(["']).*\1$/.test(core)) kind = 'string';
		else if (NUMBER.test(core)) kind = 'number';
		else if (LITERAL.test(core)) kind = 'literal';
		else if (/^[|>][-+]?\d*$/.test(core) || /^[[{]/.test(core)) kind = 'punct';
		out.push({ kind, text: core });
	}
	if (trail) out.push({ kind: 'plain', text: trail });
	return out;
}

/** Tokens of one line. Joining their `text` gives the line back unchanged. */
export function highlightLine(line: string): Token[] {
	const out: Token[] = [];
	const c = commentStart(line);
	const code = c >= 0 ? line.slice(0, c) : line;
	const comment = c >= 0 ? line.slice(c) : '';
	// indent, optional list dashes, optional `key:` (quoted or plain), rest
	const m = /^(\s*)((?:-\s+)*)(?:("[^"]*"|'[^']*'|[^\s:#"'][^:#]*?)(\s*:)(?=\s|$))?(.*)$/.exec(
		code
	);
	if (m) {
		const [, indent, dashes, key, colon, rest] = m;
		if (indent) out.push({ kind: 'plain', text: indent });
		if (dashes) out.push({ kind: 'punct', text: dashes });
		if (key !== undefined && colon !== undefined) {
			out.push({ kind: 'key', text: key });
			out.push({ kind: 'punct', text: colon });
		}
		out.push(...valueTokens(rest ?? ''));
	} else if (code) {
		out.push({ kind: 'plain', text: code });
	}
	if (comment) out.push({ kind: 'comment', text: comment });
	return out;
}

/** Tokens per line of a whole document. */
export function highlightYaml(source: string): Token[][] {
	return source.replace(/\r\n?/g, '\n').split('\n').map(highlightLine);
}

import { describe, expect, it } from 'vitest';
import { highlightLine, highlightYaml, type TokenKind } from './yaml-highlight';

// HUB-268: YAML highlighting for the transformer viewer (text tokens only, never HTML).

const kinds = (line: string) =>
	highlightLine(line)
		.filter((t) => t.text.trim())
		.map((t) => [t.kind, t.text.trim()] as [TokenKind, string]);

describe('highlightLine', () => {
	it.each<[string, [TokenKind, string][]]>([
		[
			'transformer: T1',
			[
				['key', 'transformer'],
				['punct', ':'],
				['plain', 'T1']
			]
		],
		[
			'version: 7',
			[
				['key', 'version'],
				['punct', ':'],
				['number', '7']
			]
		],
		[
			'  idempotent: true',
			[
				['key', 'idempotent'],
				['punct', ':'],
				['literal', 'true']
			]
		],
		[
			'  path: "/1/upload"',
			[
				['key', 'path'],
				['punct', ':'],
				['string', '"/1/upload"']
			]
		],
		[
			'  - name: order_placed',
			[
				['punct', '-'],
				['key', 'name'],
				['punct', ':'],
				['plain', 'order_placed']
			]
		],
		['# a comment', [['comment', '# a comment']]],
		[
			'ts: 1793438100 # seconds',
			[
				['key', 'ts'],
				['punct', ':'],
				['number', '1793438100'],
				['comment', '# seconds']
			]
		],
		[
			'url: "https://x.test/#frag"',
			[
				['key', 'url'],
				['punct', ':'],
				['string', '"https://x.test/#frag"']
			]
		],
		[
			'body: |',
			[
				['key', 'body'],
				['punct', ':'],
				['punct', '|']
			]
		],
		[
			'  - plain item',
			[
				['punct', '-'],
				['plain', 'plain item']
			]
		],
		[
			'request:',
			[
				['key', 'request'],
				['punct', ':']
			]
		],
		[
			'evtName: ${in.event.name}',
			[
				['key', 'evtName'],
				['punct', ':'],
				['plain', '${in.event.name}']
			]
		],
		['...', [['plain', '...']]]
	])('%s', (line, want) => expect(kinds(line)).toEqual(want));

	it.each(['a: b', '  - x: "y" # z', '', '   ', 'k:v', "'q': 1", 'x: <script>alert(1)</script>'])(
		'keeps the text unchanged: %j',
		(line) =>
			expect(
				highlightLine(line)
					.map((t) => t.text)
					.join('')
			).toBe(line)
	);
});

describe('highlightYaml', () => {
	it('splits lines (CRLF too)', () => {
		expect(highlightYaml('a: 1\r\nb: 2\n')).toHaveLength(3);
	});
});

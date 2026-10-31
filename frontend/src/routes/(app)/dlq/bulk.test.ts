import { describe, expect, it } from 'vitest';
import { bulkCopy, bulkDone, bulkWarnings, deadLetters, willAffect } from './bulk';

// HUB-267: replay / purge copy, warnings and result text.

const result = { change_id: 'chg_1', matched: 42, affected: 42, truncated: false, dry_run: false };

describe('bulk copy', () => {
	it.each([
		[1, '1 dead letter'],
		[1284, '1,284 dead letters']
	])('deadLetters(%i)', (n, text) => expect(deadLetters(n)).toBe(text));

	it('names the exact count and the scope', () => {
		const replay = bulkCopy('replay', { matched: 42 }, { reason: 'UPSTREAM_REJECTED' });
		expect(replay.title).toBe('Replay 42 dead letters');
		expect(replay.consequence).toMatch(/^Moves the 42 dead letters matching these filters/);
		expect(replay.verb).toBe('Replay');
		const purge = bulkCopy('purge', { matched: 1 }, {});
		expect(purge.title).toBe('Purge 1 dead letter');
		expect(purge.consequence).toMatch(/in the DLQ \(no filter set\)/);
		expect(purge.consequence).toMatch(/request ledger and audit stay/);
	});

	it('caps one call at 10,000 and says so', () => {
		expect(willAffect({ matched: 12_500 })).toBe(10_000);
		expect(bulkCopy('replay', { matched: 12_500 }, { replayed: 'false' }).title).toBe(
			'Replay 10,000 dead letters'
		);
		expect(bulkWarnings('replay', { matched: 12_500 }, { replayed: 'false' })).toEqual([
			'12,500 dead letters match, more than the 10,000 one call handles: this replay covers the first 10,000. Count again afterwards for the rest.'
		]);
	});

	it('warns that a replay without "Not replayed yet" may send entries again', () => {
		expect(bulkWarnings('replay', { matched: 3 }, {})[0]).toMatch(/already replayed/);
		expect(bulkWarnings('replay', { matched: 3 }, { replayed: 'true' })[0]).toMatch(
			/already replayed once/
		);
		expect(bulkWarnings('replay', { matched: 3 }, { replayed: 'false' })).toEqual([]);
		expect(bulkWarnings('purge', { matched: 3 }, {})).toEqual([]);
	});

	it('reports what happened', () => {
		expect(bulkDone('replay', result)).toBe(
			'Replayed 42 dead letters. They are back in the queue and will be delivered again.'
		);
		expect(bulkDone('purge', { ...result, matched: 1, affected: 1 })).toBe(
			'Purged 1 dead letter. The request ledger and audit stay.'
		);
		expect(
			bulkDone('replay', { ...result, matched: 12_500, affected: 10_000, truncated: true })
		).toBe(
			'Replayed 10,000 dead letters (12,500 matched). They are back in the queue and will be delivered again. More entries match: count again to continue.'
		);
	});
});

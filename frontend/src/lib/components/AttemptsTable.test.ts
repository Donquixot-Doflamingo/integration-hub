import { render, screen, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import type { Attempt } from '$lib/api/types';
import AttemptsTable from './AttemptsTable.svelte';

// HUB-289: a replayed dead letter has two attempts numbered 1 (the Hub restarts the count). Keying
// rows by attempt_no crashed the DLQ entry and request pages (each_key_duplicate).
const base = contractMocks.listRequestAttempts.example.items[0]!;
const at = (over: Partial<Attempt>): Attempt => ({ ...base, ...over });

describe('AttemptsTable', () => {
	it('renders attempts with the same number, oldest first', () => {
		render(AttemptsTable, {
			props: {
				scoped: false,
				attempts: [
					at({ attempt_no: 1, started_at: '2026-11-06T23:07:25Z', vendor_status: 200 }),
					at({ attempt_no: 1, started_at: '2026-11-06T22:33:10Z', vendor_status: 400 })
				]
			}
		});
		const rows = within(screen.getByRole('table')).getAllByRole('row').slice(1);
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent('400');
		expect(rows[1]).toHaveTextContent('200');
	});

	it('orders by attempt number when start times tie', () => {
		const t = '2026-11-06T22:33:10Z';
		render(AttemptsTable, {
			props: {
				scoped: true,
				attempts: [at({ attempt_no: 2, started_at: t }), at({ attempt_no: 1, started_at: t })]
			}
		});
		const heads = within(screen.getByRole('table'))
			.getAllByRole('rowheader')
			.map((h) => h.textContent?.trim());
		expect(heads).toEqual(['1', '2']);
	});
});

import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles } from '$lib/auth/roles';
import Page from './+page.svelte';
import { load } from './+page';

// HUB-274: filterable change log with filters in the URL.

const goto = vi.fn();
vi.mock('$app/navigation', () => ({
	goto: (...a: unknown[]) => goto(...a),
	invalidateAll: vi.fn()
}));

function renderAt(search: string) {
	const data = load({
		fetch,
		url: new URL(`http://console.test/change-log${search}`)
	} as never) as object;
	return render(Page, {
		props: { data: { access: accessFromRoles(['viewer']), ...data } as never }
	});
}

beforeEach(() => {
	goto.mockReset();
	server.events.removeAllListeners();
});

describe('change log list', () => {
	it('sends the URL filters, shows chips and links entries and objects', async () => {
		const seen: URLSearchParams[] = [];
		server.events.on('request:start', ({ request }) => {
			const u = new URL(request.url);
			if (u.pathname.endsWith('/admin/v1/change-log')) seen.push(u.searchParams);
		});
		renderAt('?actor=priya%40example.com&object_type=integration');
		const table = await screen.findByRole('table', { busy: false });
		expect(Object.fromEntries(seen[0]!)).toEqual({
			actor: 'priya@example.com',
			object_type: 'integration',
			limit: '50'
		});
		expect(
			screen.getByRole('link', { name: 'Remove filter Object type: Integration' })
		).toHaveAttribute('href', '/change-log?actor=priya%40example.com');
		expect(
			within(table).getByRole('link', { name: 'chg_01J9Z4B2C3D4E5F6G7H8J9K0N5' })
		).toHaveAttribute('href', '/change-log/chg_01J9Z4B2C3D4E5F6G7H8J9K0N5');
		expect(within(table).getByRole('link', { name: 'int_evt_brandA' })).toHaveAttribute(
			'href',
			'/integrations/int_evt_brandA'
		);
		const row = within(table).getAllByRole('row')[1]!;
		expect(row).toHaveTextContent('priya@example.com (editor)');
		expect(row).toHaveTextContent('Canary 20% to the new workspace');
		expect(row).toHaveTextContent('413');
		expect(screen.getByRole('navigation', { name: 'Pagination' })).toBeInTheDocument();
	});

	it('applies the form as a URL', async () => {
		renderAt('');
		await screen.findByRole('table', { busy: false });
		await userEvent.type(screen.getByLabelText('Object id'), 'orders-service');
		await userEvent.selectOptions(screen.getByLabelText('Object type'), 'Tenant');
		await userEvent.click(screen.getByRole('button', { name: 'Apply' }));
		expect(goto).toHaveBeenCalledWith('/change-log?object_type=tenant&object_id=orders-service', {
			keepFocus: true,
			noScroll: true
		});
	});

	it('refuses a reversed range without calling the API', () => {
		const calls: string[] = [];
		server.events.on('request:start', ({ request }) => calls.push(request.url));
		renderAt('?from=2026-11-01T00:00:00Z&to=2026-10-31T00:00:00Z');
		expect(screen.getByRole('alert')).toHaveTextContent('"To" must be later than "From".');
		expect(screen.getByLabelText('From (UTC)')).toHaveAttribute('aria-invalid', 'true');
		expect(calls).toEqual([]);
	});
});

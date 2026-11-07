import { render, screen, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import { server } from '$lib/api/mocks/server';
import Page from './+page.svelte';
import { load } from './+page';

// HUB-268: transformers list with URL filters.

const goto = vi.fn();
vi.mock('$app/navigation', () => ({
	goto: (...a: unknown[]) => goto(...a),
	invalidateAll: vi.fn()
}));

function renderAt(search: string, access: Access = accessFromRoles(['viewer'])) {
	const data = load({
		fetch,
		url: new URL(`http://console.test/transformers${search}`)
	} as never) as object;
	return render(Page, { props: { data: { access, ...data } as never } });
}

beforeEach(() => {
	goto.mockReset();
	server.events.removeAllListeners();
});

describe('transformers list', () => {
	it('sends the URL filters, shows chips and links each transformer', async () => {
		const seen: URLSearchParams[] = [];
		server.events.on('request:start', ({ request }) => {
			const u = new URL(request.url);
			if (u.pathname.endsWith('/admin/v1/transformers')) seen.push(u.searchParams);
		});
		renderAt('?kind=request&vendor_id=clevertap');
		const table = await screen.findByRole('table', { busy: false });
		expect(Object.fromEntries(seen[0]!)).toEqual({
			kind: 'request',
			vendor_id: 'clevertap',
			limit: '50'
		});
		expect(
			screen.getByRole('link', {
				name: 'Remove filter Kind: Request (caller input to vendor request)'
			})
		).toHaveAttribute('href', '/transformers?vendor_id=clevertap');
		expect(within(table).getByRole('link', { name: 'T1' })).toHaveAttribute(
			'href',
			'/transformers/T1'
		);
	});

	it('applies the form as a URL', async () => {
		renderAt('');
		await screen.findByRole('table', { busy: false });
		(screen.getByLabelText('Kind') as HTMLSelectElement).value = 'response';
		screen.getByRole('button', { name: 'Apply' }).click();
		expect(goto).toHaveBeenCalledWith('/transformers?kind=response', {
			keepFocus: true,
			noScroll: true
		});
	});

	it('tells tenant viewers why the list is not for them', () => {
		renderAt('', accessFromRoles(['tenant_viewer'], ['orders-service']));
		expect(screen.getByRole('note')).toHaveTextContent('tenant viewers cannot see');
		expect(screen.queryByRole('table')).toBeNull();
	});
});

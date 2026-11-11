import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { SwitchTarget } from '$lib/auth/tenant-choice';
import TenantSwitcher from './TenantSwitcher.svelte';

// HUB-494: the tenant switcher in the top bar.
const invalidateAll = vi.fn(async () => {});
vi.mock('$app/navigation', () => ({ invalidateAll: () => invalidateAll() }));

const P = '11111111-1111-4111-8111-111111111111';
const O = '22222222-2222-4222-8222-222222222222';
const B = '33333333-3333-4333-8333-333333333333';
const choices: SwitchTarget[] = [
	{ id: P, key: 'INTEGRATION_HUB_PLATFORM', name: 'Integration Hub platform', platform: true },
	{ id: O, key: 'ORDERS', name: 'Orders service', platform: false },
	{ id: B, key: 'BILLING', name: 'BILLING', platform: false }
];

beforeEach(() => invalidateAll.mockClear());

describe('TenantSwitcher', () => {
	it('lists the tenants with the platform tenant first and the current one selected', () => {
		render(TenantSwitcher, { props: { choices, currentId: O, next: '/vendors' } });
		const select = screen.getByRole('combobox', { name: 'Tenant' });
		expect(select).toHaveValue(O);
		expect(screen.getAllByRole('option').map((o) => o.textContent?.trim())).toEqual([
			'Integration Hub platform (INTEGRATION_HUB_PLATFORM), platform',
			'Orders service (ORDERS)',
			'BILLING'
		]);
	});

	it('is a form that posts to /tenant and returns to the page (works without JavaScript)', () => {
		render(TenantSwitcher, { props: { choices, currentId: P, next: '/dlq?tenant_id=a' } });
		const form = screen.getByRole('combobox').closest('form')!;
		expect(form).toHaveAttribute('method', 'POST');
		expect(form).toHaveAttribute('action', '/tenant');
		expect(form.querySelector('input[name=next]')).toHaveValue('/dlq?tenant_id=a');
		expect(screen.getByRole('combobox')).toHaveAttribute('name', 'tenant_id');
	});

	it('picking a tenant posts it, then reloads every load function (caches and /me)', async () => {
		const fetchMock = vi.fn(async () => Response.json({ ok: true, tenantId: O }));
		vi.stubGlobal('fetch', fetchMock);
		render(TenantSwitcher, { props: { choices, currentId: P, next: '/vendors' } });
		await userEvent.selectOptions(screen.getByRole('combobox'), O);
		await waitFor(() => expect(invalidateAll).toHaveBeenCalledOnce());
		const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
		expect(url).toBe('/tenant');
		expect(init.method).toBe('POST');
		expect((init.headers as Record<string, string>).accept).toBe('application/json');
		expect(String(init.body)).toBe(`tenant_id=${O}&next=%2Fvendors`);
	});

	it('shows the refusal and goes back to the current tenant when the switch fails', async () => {
		vi.stubGlobal(
			'fetch',
			vi.fn(async () =>
				Response.json({ ok: false, message: 'You have no access to that tenant.' }, { status: 403 })
			)
		);
		render(TenantSwitcher, { props: { choices, currentId: P, next: '/' } });
		await userEvent.selectOptions(screen.getByRole('combobox'), B);
		expect(await screen.findByRole('alert')).toHaveTextContent(
			'You have no access to that tenant.'
		);
		expect(screen.getByRole('combobox')).toHaveValue(P);
		expect(invalidateAll).not.toHaveBeenCalled();
	});

	it('says so when the request itself fails', async () => {
		vi.stubGlobal(
			'fetch',
			vi.fn(async () => Promise.reject(new TypeError('offline')))
		);
		render(TenantSwitcher, { props: { choices, currentId: P, next: '/' } });
		await userEvent.selectOptions(screen.getByRole('combobox'), O);
		expect(await screen.findByRole('alert')).toHaveTextContent('could not be switched');
	});

	it('shows nothing when ISFC lists no tenants, and says why when the list failed', () => {
		const { unmount } = render(TenantSwitcher, {
			props: { choices: [], currentId: null, next: '/' }
		});
		expect(screen.queryByRole('combobox')).toBeNull();
		unmount();
		render(TenantSwitcher, {
			props: { choices: [], currentId: null, error: 'ISFC did not return your tenants.', next: '/' }
		});
		expect(screen.getByRole('status')).toHaveTextContent('did not return your tenants');
	});
});

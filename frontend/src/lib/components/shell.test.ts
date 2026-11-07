import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { adminClient } from '$lib/api/client';
import { HubApiError } from '$lib/api/errors';
import { mockError } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import Breadcrumbs from './Breadcrumbs.svelte';
import ErrorPanel from './ErrorPanel.svelte';
import SideNav from './SideNav.svelte';
import UserMenu from './UserMenu.svelte';

const api = () => adminClient({ baseUrl: 'http://hub.test/admin/v1' });

describe('SideNav', () => {
	it('links every section and marks the active one', () => {
		render(SideNav, { props: { pathname: '/dlq' } });
		const nav = screen.getByRole('navigation', { name: 'Sections' });
		const links = within(nav).getAllByRole('link');
		expect(links.map((l) => l.textContent?.trim())).toEqual([
			'Integrations',
			'Health',
			'Requests',
			'DLQ',
			'Transformers',
			'Vendors',
			'Accounts',
			'Tenants',
			'Snapshots',
			'Change log'
		]);
		expect(within(nav).getByRole('link', { name: 'DLQ' })).toHaveAttribute('aria-current', 'page');
		expect(within(nav).getByRole('link', { name: 'Health' })).not.toHaveAttribute('aria-current');
	});

	it('keeps the section active on nested routes', () => {
		render(SideNav, { props: { pathname: '/integrations/int_evt_brandA' } });
		expect(screen.getByRole('link', { name: 'Integrations' })).toHaveAttribute(
			'aria-current',
			'page'
		);
	});
});

describe('Breadcrumbs', () => {
	it('renders the trail with the current page last', () => {
		render(Breadcrumbs, { props: { pathname: '/integrations/int_evt_brandA' } });
		const nav = screen.getByRole('navigation', { name: 'Breadcrumb' });
		const links = within(nav).getAllByRole('link');
		expect(links.map((l) => l.textContent)).toEqual(['Console', 'Integrations', 'int_evt_brandA']);
		expect(links.at(-1)).toHaveAttribute('aria-current', 'page');
	});
});

describe('UserMenu', () => {
	const session = {
		user: {
			subject: 'dev|ops@example.com',
			name: 'Ops Person',
			email: 'ops@example.com',
			roles: ['viewer' as const],
			tenants: []
		},
		provider: 'dev' as const,
		expiresAt: '2026-10-31T18:00:00Z'
	};

	it('shows the user from /me, opens and closes by keyboard, and offers sign out', async () => {
		const user = userEvent.setup();
		const me = await api().call((c) => c.GET('/me'));
		render(UserMenu, { props: { me, ...session } });

		const trigger = screen.getByRole('button', { name: /Priya Sharma/ });
		expect(trigger).toHaveAttribute('aria-expanded', 'false');
		expect(trigger).toHaveTextContent('Editor');
		expect(screen.getByText('PS')).toBeInTheDocument();

		trigger.focus();
		await user.keyboard('{Enter}');
		expect(trigger).toHaveAttribute('aria-expanded', 'true');
		expect(screen.getByText('priya@example.com')).toBeVisible();
		expect(screen.getByText('All tenants')).toBeVisible();
		expect(screen.getByText('Development role picker')).toBeVisible();
		const signOut = screen.getByRole('button', { name: 'Sign out' });
		expect(signOut.closest('form')).toHaveAttribute('action', '/logout');
		expect(signOut.closest('form')).toHaveAttribute('method', 'POST');

		await user.keyboard('{Escape}');
		expect(trigger).toHaveAttribute('aria-expanded', 'false');
		expect(trigger).toHaveFocus();
	});

	it('falls back to the session identity when /me fails, and says changes are hidden', async () => {
		server.use(mockError('getMe', 500));
		const me = await api().call((c) => c.GET('/me'));
		render(UserMenu, { props: { me, ...session } });
		expect(screen.getByRole('button', { name: /Ops Person/ })).toHaveTextContent('Viewer');
		expect(
			screen.getByText(/Could not load your roles .*INTERNAL_ERROR/, { selector: 'p' })
		).toBeInTheDocument();
	});
});

describe('ErrorPanel', () => {
	const error = new HubApiError({
		status: 403,
		code: 'FORBIDDEN',
		message: "Requires role 'admin'",
		requestId: 'req_01J9Z3M1A5B7C9D1E3F5G7H9KE'
	});

	it('shows code, message and request id, and retries', async () => {
		const user = userEvent.setup();
		const onretry = vi.fn();
		render(ErrorPanel, { props: { error, context: 'Could not load tenants', onretry } });
		const alert = screen.getByRole('alert');
		expect(alert).toHaveTextContent('Could not load tenants');
		expect(alert).toHaveTextContent("Requires role 'admin'");
		expect(alert).toHaveTextContent('FORBIDDEN');
		expect(alert).toHaveTextContent('req_01J9Z3M1A5B7C9D1E3F5G7H9KE');
		expect(alert).toHaveTextContent(/Ask an admin/);
		await user.click(screen.getByRole('button', { name: 'Retry' }));
		expect(onretry).toHaveBeenCalledOnce();
	});

	it('copies the request id and announces it', async () => {
		const user = userEvent.setup();
		const writeText = vi.fn().mockResolvedValue(undefined);
		Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
		render(ErrorPanel, { props: { error, context: 'Could not load tenants' } });
		await user.click(screen.getByRole('button', { name: 'Copy request id' }));
		expect(writeText).toHaveBeenCalledWith('req_01J9Z3M1A5B7C9D1E3F5G7H9KE');
		expect(await screen.findByText('Copied')).toBeInTheDocument();
	});

	it('explains a missing request id for network failures', () => {
		render(ErrorPanel, {
			props: { error: HubApiError.network(new TypeError('x')), context: 'Could not load DLQ' }
		});
		expect(screen.getByRole('alert')).toHaveTextContent('did not reach the Hub');
		expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
	});
});

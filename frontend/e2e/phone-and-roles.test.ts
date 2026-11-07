import { expect, test, type Page } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn, type Role } from './helpers';

// HUB-287: no screen scrolls sideways on a 390 px phone; wide tables scroll inside their own
// container and long ids wrap. The breadcrumb becomes one "back to the parent" link.
// HUB-288: destructive controls are absent (not disabled) for roles the contract does not allow.

const PAGES = [
	'/integrations',
	'/integrations/int_evt_brandA',
	'/integrations/new',
	'/integrations/int_evt_brandA/publish',
	'/integrations/int_evt_brandA/diff?from=7&to=8',
	'/integrations/int_evt_brandA/diff?from=7&to=8&view=paths',
	'/health',
	'/requests',
	'/requests/req_01J9Z3K8X2V7Q4M6N8P0R2S4T6',
	'/dlq',
	'/dlq/req_01J9Z3M1A5B7C9D1E3F5G7H9JK',
	'/transformers',
	'/transformers/T1',
	'/transformers/new',
	'/transformers/T1/new-version',
	'/vendors',
	'/vendors/new',
	'/vendors/clevertap',
	'/vendors/clevertap/edit',
	'/vendors/clevertap/endpoints/upload@1',
	'/vendors/clevertap/endpoints/new',
	'/vendors/clevertap/endpoints/new?from=upload',
	'/accounts',
	'/accounts/new',
	'/accounts/ct-brandA-in',
	'/tenants',
	'/tenants/orders-service',
	'/snapshots',
	'/change-log',
	'/change-log/chg_01J9Z4B2C3D4E5F6G7H8J9K0N5'
];

async function sideways(page: Page) {
	await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
	return page.evaluate(
		() => document.documentElement.scrollWidth - document.documentElement.clientWidth
	);
}

test.describe('390 px phone', () => {
	test.use({ viewport: { width: 390, height: 844 } });

	test('no page scrolls sideways', async ({ page }) => {
		await signIn(page, { role: 'admin' });
		for (const path of PAGES) {
			await page.goto(path);
			await expect(page.locator('main h1')).toBeVisible();
			expect({ path, overflow: await sideways(page) }).toEqual({ path, overflow: 0 });
		}
	});

	test('the breadcrumb is a single link back to the parent', async ({ page }) => {
		await signIn(page, { role: 'admin' });
		await page.goto('/integrations/int_evt_brandA');
		const crumbs = page.getByRole('navigation', { name: 'Breadcrumb' });
		await expect(crumbs.getByRole('link', { name: 'Integrations' })).toBeVisible();
		await expect(crumbs.getByRole('link', { name: 'Console' })).toBeHidden();
		await expect(crumbs.getByRole('link', { name: 'int_evt_brandA' })).toBeHidden();
		await expectNoSeriousA11yViolations(page);
	});
});

// `link`: controls that open their own page (HUB-436: endpoint versions are created on a page).
const DESTRUCTIVE: { path: string; name: RegExp; min: Role; role?: 'button' | 'link' }[] = [
	{ path: '/dlq', name: /^Purge matching/, min: 'admin' },
	{ path: '/dlq', name: /^Replay matching/, min: 'editor' },
	{ path: '/health', name: /^Force open$/, min: 'admin' },
	{ path: '/integrations/int_evt_brandA', name: /Pause integration/, min: 'editor' },
	{ path: '/tenants/orders-service', name: /^Delete reg_/, min: 'admin' },
	{ path: '/tenants/orders-service', name: /^Revoke hk_/, min: 'admin' },
	{ path: '/tenants/orders-service', name: /^Disable tenant$/, min: 'admin' },
	{ path: '/vendors/clevertap', name: /^Add endpoint$/, min: 'admin', role: 'link' },
	{ path: '/vendors/clevertap', name: /^New version of upload/, min: 'admin', role: 'link' },
	{ path: '/accounts/ct-brandA-in', name: /^Add next credential$/, min: 'admin' },
	{ path: '/accounts/ct-brandA-in', name: /^Start draining$/, min: 'admin' },
	{ path: '/accounts/ct-brandA-in', name: /^Test connection$/, min: 'editor' }
];
const RANK: Record<Role, number> = { tenant_viewer: 0, viewer: 1, editor: 2, admin: 3 };

for (const role of ['admin', 'editor', 'viewer', 'tenant_viewer'] as const) {
	test(`${role} sees only the destructive controls its role allows`, async ({ page }) => {
		await signIn(page, {
			role,
			tenants: role === 'tenant_viewer' ? 'orders-service' : undefined
		});
		for (const c of DESTRUCTIVE) {
			await page.goto(c.path);
			await expect(page.locator('main h1')).toBeVisible();
			await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
			const control = page.getByRole(c.role ?? 'button', { name: c.name });
			if (RANK[role] >= RANK[c.min]) await expect(control.first()).toBeVisible();
			else await expect(control).toHaveCount(0);
		}
	});
}

import AxeBuilder from '@axe-core/playwright';
import { expect, type Page } from '@playwright/test';

export type Role = 'admin' | 'editor' | 'viewer' | 'tenant_viewer';

/** Signs in through the dev login page (mock builds use the dev provider, HUB-253). */
export async function signIn(
	page: Page,
	opts: { role?: Role; next?: string; tenants?: string; name?: string } = {}
) {
	const { role = 'editor', next, tenants, name } = opts;
	await page.goto(next ? `/login?next=${encodeURIComponent(next)}` : '/login');
	await page.getByRole('radio', { name: new RegExp(`^${roleLabel(role)}\\b`) }).check();
	if (tenants !== undefined) await page.getByLabel(/^Tenants/).fill(tenants);
	if (name !== undefined) await page.getByLabel('Name').fill(name);
	await page.getByRole('button', { name: 'Sign in' }).click();
	// The shell is up (the rail may be a closed drawer at narrow widths, so check the user menu).
	await expect(page).not.toHaveURL(/\/login/);
	await expect(page.getByRole('button', { name: /Account details/ })).toBeVisible();
}

export function roleLabel(role: Role): string {
	return { admin: 'Admin', editor: 'Editor', viewer: 'Viewer', tenant_viewer: 'Tenant viewer' }[
		role
	];
}

export async function expectNoSeriousA11yViolations(page: Page) {
	const results = await new AxeBuilder({ page }).analyze();
	const bad = results.violations.filter((v) => ['serious', 'critical'].includes(v.impact ?? ''));
	expect(bad.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)).toEqual(
		[]
	);
}

/** Selects a mock scenario (see src/lib/api/mocks/scenarios.ts) for browser and server mocks. */
export async function useScenario(page: Page, scenario: string) {
	await page
		.context()
		.addCookies([{ name: 'hub_mock_scenario', value: scenario, domain: 'localhost', path: '/' }]);
}

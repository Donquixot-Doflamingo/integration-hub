import AxeBuilder from '@axe-core/playwright';
import { expect, type Page } from '@playwright/test';

export type Role = 'admin' | 'editor' | 'viewer' | 'tenant_viewer' | 'tenant_admin';

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
	return {
		admin: 'Admin',
		editor: 'Editor',
		viewer: 'Viewer',
		tenant_viewer: 'Tenant viewer',
		tenant_admin: 'Tenant admin'
	}[role];
}

export async function expectNoSeriousA11yViolations(page: Page) {
	// AsyncView dims data while it refreshes (aria-busy, opacity 0.6), e.g. right after a mutation
	// reloads the page data; axe would measure that transient state's contrast. Wait it out.
	// Checking aria-busy alone races: a reload triggered by the mutation may not have started yet
	// when it is checked, then dims the page while axe runs. Wait for the network to go quiet, then
	// for no busy region and no dimmed data block.
	await page.waitForLoadState('networkidle');
	await expect
		.poll(() =>
			page.evaluate(
				() =>
					!document.querySelector('[aria-busy="true"]') &&
					[...document.querySelectorAll('.data')].every(
						(el) => getComputedStyle(el).opacity === '1'
					)
			)
		)
		.toBe(true);
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

/**
 * Every place in the browser a secret shown once could still be after its reveal closed
 * (API keys HUB-271, callback signing secrets HUB-272).
 */
export async function lingering(page: Page, secret: string) {
	return page.evaluate((s) => {
		const values = [...document.querySelectorAll('input, textarea')].map(
			(e) => (e as HTMLInputElement).value
		);
		const storage = [localStorage, sessionStorage].flatMap((st) =>
			Object.keys(st).map((k) => `${k}=${st.getItem(k)}`)
		);
		return {
			html: document.documentElement.outerHTML.includes(s),
			inputs: values.some((v) => v.includes(s)),
			storage: storage.some((v) => v.includes(s)),
			url: location.href.includes(s),
			history: JSON.stringify(history.state ?? null).includes(s)
		};
	}, secret);
}
export const NOWHERE = { html: false, inputs: false, storage: false, url: false, history: false };

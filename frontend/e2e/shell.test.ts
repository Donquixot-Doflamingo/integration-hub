import { expect, test } from '@playwright/test';
import { expectNoSeriousA11yViolations, signIn } from './helpers';

// HUB-254: app shell on MSW mocks (build:mock). Navigation, active route, keyboard reachability,
// 1280 px and 768 px layouts, light and dark themes, axe (no serious/critical violations).

test.beforeEach(async ({ page }) => signIn(page));

const SECTIONS = [
	{ label: 'Integrations', path: '/integrations', row: 'int_evt_brandA' },
	{ label: 'Health', path: '/health', row: 'clevertap' },
	{ label: 'Requests', path: '/requests', row: 'req_01J9Z3K8X2V7Q4M6N8P0R2S4T6' },
	{ label: 'DLQ', path: '/dlq', row: 'req_01J9Z3M1A5B7C9D1E3F5G7H9JK' },
	{ label: 'Transformers', path: '/transformers', row: 'T1' },
	{ label: 'Tenants', path: '/tenants', row: 'orders-service' },
	{ label: 'Snapshots', path: '/snapshots', row: '413' },
	{ label: 'Change log', path: '/change-log', row: 'chg_01J9Z4B2C3D4E5F6G7H8J9K0N5' }
];

for (const scheme of ['light', 'dark'] as const) {
	test.describe(`1280 px, ${scheme}`, () => {
		test.use({ viewport: { width: 1280, height: 800 }, colorScheme: scheme });

		test('every section is reachable from the sidebar and loads its list', async ({ page }) => {
			await page.goto('/');
			await expect(page.locator('html')).toHaveAttribute('data-theme', scheme);
			await expect(page.getByRole('button', { name: /Priya Sharma/ })).toBeVisible();
			const nav = page.getByRole('navigation', { name: 'Sections' });
			for (const s of SECTIONS) {
				await nav.getByRole('link', { name: s.label, exact: true }).click();
				await expect(page).toHaveURL(s.path);
				await expect(page.getByRole('heading', { level: 1 })).toHaveText(s.label);
				await expect(nav.getByRole('link', { name: s.label, exact: true })).toHaveAttribute(
					'aria-current',
					'page'
				);
				await expect(
					page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: s.label })
				).toHaveAttribute('aria-current', 'page');
				await expect(page.getByRole('rowheader').first()).toContainText(s.row);
				await expectNoSeriousA11yViolations(page);
			}
		});
	});
}

test.describe('keyboard', () => {
	test.use({ viewport: { width: 1280, height: 800 } });

	test('skip link, every section link and the header tools are reachable by Tab', async ({
		page
	}) => {
		await page.goto('/integrations');
		await expect(page.getByRole('rowheader').first()).toBeVisible();

		await page.keyboard.press('Tab');
		await expect(page.getByRole('link', { name: 'Skip to content' })).toBeFocused();
		await page.keyboard.press('Tab');
		await expect(page.getByRole('link', { name: /Integration Hub/ })).toBeFocused();
		for (const s of SECTIONS) {
			await page.keyboard.press('Tab');
			await expect(page.getByRole('link', { name: s.label, exact: true }).first()).toBeFocused();
		}
		// Enter follows the focused link.
		await page.keyboard.press('Enter');
		await expect(page).toHaveURL('/change-log');

		// Theme radios and the user menu are keyboard operable.
		await page.getByRole('radio', { name: 'Dark' }).focus();
		await page.keyboard.press('Space');
		await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
		const user = page.getByRole('button', { name: /Priya Sharma/ });
		await user.focus();
		await page.keyboard.press('Enter');
		await expect(page.getByText('priya@example.com')).toBeVisible();
		await page.keyboard.press('Escape');
		await expect(page.getByText('priya@example.com')).toBeHidden();
		await expect(user).toBeFocused();
	});

	test('theme choice persists across reloads', async ({ page }) => {
		await page.goto('/');
		await page.getByRole('radio', { name: 'Dark' }).check();
		await page.reload();
		await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
		await expect(page.getByRole('radio', { name: 'Dark' })).toBeChecked();
	});
});

test.describe('768 px', () => {
	test.use({ viewport: { width: 768, height: 1024 } });

	test('sections drawer opens, navigates, closes on Escape and returns focus', async ({ page }) => {
		await page.goto('/health');
		const nav = page.getByRole('navigation', { name: 'Sections' });
		const toggle = page.getByRole('button', { name: 'Sections' });
		await expect(nav).toBeHidden();
		await expect(toggle).toHaveAttribute('aria-expanded', 'false');

		await toggle.click();
		await expect(nav).toBeVisible();
		await expect(nav.getByRole('link', { name: 'Health' })).toHaveAttribute('aria-current', 'page');
		await page.keyboard.press('Escape');
		await expect(nav).toBeHidden();
		await expect(toggle).toBeFocused();

		await page.keyboard.press('Enter');
		await expect(nav).toBeVisible();
		await nav.getByRole('link', { name: 'Tenants' }).click();
		await expect(page).toHaveURL('/tenants');
		await expect(nav).toBeHidden();
		await expect(page.getByRole('rowheader').first()).toContainText('orders-service');

		// The page itself never scrolls sideways; wide tables scroll inside their container.
		const overflow = await page.evaluate(
			() => document.documentElement.scrollWidth - document.documentElement.clientWidth
		);
		expect(overflow).toBeLessThanOrEqual(0);
		await expectNoSeriousA11yViolations(page);
	});
});

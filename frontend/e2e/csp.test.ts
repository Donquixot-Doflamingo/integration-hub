import { expect, test } from '@playwright/test';
import { signIn } from './helpers';

// HUB-499 (gap G12): the console sends a nonce-based CSP and none of its pages breaks under it.
test.describe('content security policy', () => {
	test('page responses carry a strict policy', async ({ page }) => {
		const res = await page.goto('/login');
		const csp = res?.headers()['content-security-policy'] ?? '';
		expect(csp).toContain("default-src 'self'");
		expect(csp).toContain("frame-ancestors 'none'");
		const scriptSrc = csp.split(';').find((d) => d.trim().startsWith('script-src')) ?? '';
		expect(scriptSrc).not.toContain('unsafe-inline');
		expect(scriptSrc).not.toContain('unsafe-eval');
		expect(scriptSrc).toMatch(/'(nonce|sha256)-/);
	});

	test('signed-in pages raise no CSP violation', async ({ page }) => {
		const violations: string[] = [];
		const consoleErrors: string[] = [];
		page.on('console', (m) => {
			if (/content security policy/i.test(m.text())) consoleErrors.push(m.text());
		});
		await page.addInitScript(() => {
			(window as unknown as { __csp: string[] }).__csp = [];
			document.addEventListener('securitypolicyviolation', (e) =>
				(window as unknown as { __csp: string[] }).__csp.push(
					`${e.violatedDirective} ${e.blockedURI}`
				)
			);
		});
		await signIn(page, { role: 'admin' });
		for (const path of ['/', '/integrations', '/tenants', '/requests', '/health', '/change-log']) {
			await page.goto(path);
			await page.waitForLoadState('networkidle');
			violations.push(
				...(await page.evaluate(() => (window as unknown as { __csp: string[] }).__csp))
			);
		}
		expect(violations).toEqual([]);
		expect(consoleErrors).toEqual([]);
	});
});

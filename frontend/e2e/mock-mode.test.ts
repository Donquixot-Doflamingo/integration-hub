import { expect, test } from '@playwright/test';
import { signIn } from './helpers';

// HUB-252: the console runs with no backend; MSW answers /admin/v1 from the contract examples.
test('admin API is served by MSW from contract examples', async ({ page }) => {
	await signIn(page);
	await page.goto('/');
	await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
	const me = await page.evaluate(async () => {
		const r = await fetch('/admin/v1/me');
		return { status: r.status, requestId: r.headers.get('X-Hub-Request-Id'), body: await r.json() };
	});
	expect(me.status).toBe(200);
	expect(me.requestId).toMatch(/^req_[0-9A-HJKMNP-TV-Z]{26}$/);
	// After dev sign-in, MSW's /me answers as the chosen identity (HUB-253).
	expect(me.body).toMatchObject({ name: 'Priya Sharma', roles: ['editor'], tenants: [] });
});

import { defineConfig, devices } from '@playwright/test';

/**
 * e2e against a real Hub (HUB-290..293): the console started by `scripts/dev-up.sh` (real admin
 * API, Postgres, Redis, mockvendor), signed in through the admin's dev issuer. Nothing is mocked.
 *
 *   scripts/dev-up.sh                      # once, from the repo root
 *   npm run test:e2e:real                  # HUB_E2E_BASE_URL=http://localhost:<console port>
 *
 * Every test creates its own objects with a unique suffix, so runs never collide and can repeat
 * on the same stack. `HUB_E2E_SHOTS=<dir>` also saves desktop and phone screenshots of each page.
 */
export default defineConfig({
	testDir: 'e2e-real',
	fullyParallel: false,
	workers: 1,
	forbidOnly: !!process.env.CI,
	retries: 0,
	timeout: 90_000,
	expect: { timeout: 15_000 },
	reporter: 'list',
	use: {
		baseURL: process.env.HUB_E2E_BASE_URL ?? 'http://localhost:5173',
		trace: 'retain-on-failure',
		viewport: { width: 1280, height: 900 }
	},
	projects: [
		{
			name: 'chromium',
			use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 900 } }
		}
	]
});

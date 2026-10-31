import { defineConfig, devices } from '@playwright/test';

const PORT = 4173;

export default defineConfig({
	testDir: 'e2e',
	fullyParallel: true,
	forbidOnly: !!process.env.CI,
	retries: 0,
	// CI (HUB-280): annotations on the PR, a log, and an HTML report the workflow uploads on failure.
	reporter: process.env.CI
		? [['github'], ['list'], ['html', { open: 'never', outputFolder: 'playwright-report' }]]
		: 'list',
	use: {
		baseURL: `http://localhost:${PORT}`,
		trace: 'retain-on-failure'
	},
	projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
	webServer: {
		// e2e runs the console on MSW contract mocks (no backend), like `npm run dev:mock`.
		command: `npm run build:mock && npm run preview -- --port ${PORT} --strictPort`,
		port: PORT,
		reuseExistingServer: !process.env.CI,
		timeout: 120_000
	}
});

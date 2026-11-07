import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { expect, type Page } from '@playwright/test';

export { expectNoSeriousA11yViolations, signIn, type Role } from '../e2e/helpers';

/**
 * Helpers for e2e against the real stack (HUB-290..293). Ids get a per-run suffix so tests can
 * run again on the same stack without colliding (slugs allow letters, digits, "_" and "-").
 */
export const RUN = Date.now().toString(36);
export const uid = (prefix: string) => `${prefix}-${RUN}`;

/** Endpoint and operation names are lower-case with "_" only. */
export const snake = (prefix: string) => `${prefix}_${RUN}`;

const SHOTS = process.env.HUB_E2E_SHOTS;

/**
 * Saves `<name>.desktop.png` and `<name>.mobile.png` (390 px) of the current page when
 * `HUB_E2E_SHOTS` names a directory; otherwise does nothing.
 */
export async function shot(page: Page, name: string) {
	if (!SHOTS) return;
	mkdirSync(SHOTS, { recursive: true });
	await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
	const size = page.viewportSize() ?? { width: 1280, height: 900 };
	// The viewport grows to the page height instead of `fullPage`, so the sticky header and the
	// phone drawer render where an operator sees them.
	const tall = async (width: number, file: string) => {
		await page.setViewportSize({ width, height: 900 });
		await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
		const height = await page.evaluate(() => document.documentElement.scrollHeight);
		await page.setViewportSize({ width, height: Math.min(Math.max(height, 700), 4000) });
		await page.screenshot({ path: join(SHOTS, file) });
	};
	await tall(1280, `${name}.desktop.png`);
	await tall(390, `${name}.mobile.png`);
	await page.setViewportSize(size);
}

/** The change log id in the success notice (`chg_` + ULID). */
export async function changeIdOf(page: Page, text: RegExp | string): Promise<string> {
	const notice = page.getByRole('status').filter({ hasText: text });
	await expect(notice).toContainText(/chg_[0-9A-HJKMNP-TV-Z]{26}/);
	return (/chg_[0-9A-HJKMNP-TV-Z]{26}/.exec((await notice.textContent()) ?? '') ?? [''])[0];
}

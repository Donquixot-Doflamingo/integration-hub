import { vi } from 'vitest';

/**
 * Test helpers for secrets shown once (API keys HUB-271, callback signing secrets HUB-272):
 * capture every console call, then list every place a secret could still be after the reveal.
 */
const LOG_METHODS = ['log', 'info', 'warn', 'error', 'debug', 'trace'] as const;

/** Spies on every console method; returns the captured calls (call in `beforeEach`). */
export function captureConsole(): unknown[][] {
	const logged: unknown[][] = [];
	for (const m of LOG_METHODS) {
		vi.spyOn(console, m).mockImplementation((...args: unknown[]) => {
			logged.push(args);
		});
	}
	return logged;
}

export const NOWHERE = {
	dom: false,
	text: false,
	inputs: false,
	storage: false,
	url: false,
	history: false,
	logs: false
};

/** Where `secret` still is: DOM, text, input values, web storage, URL, history state, console. */
export function secretTraces(secret: string, logged: unknown[][]): typeof NOWHERE {
	const inputs = [...document.querySelectorAll('input, textarea')].map(
		(e) => (e as HTMLInputElement).value
	);
	const storage = [localStorage, sessionStorage].flatMap((s) =>
		Object.keys(s).map((k) => `${k}=${s.getItem(k)}`)
	);
	return {
		dom: document.documentElement.outerHTML.includes(secret),
		text: document.body.textContent?.includes(secret) ?? false,
		inputs: inputs.some((v) => v.includes(secret)),
		storage: storage.some((v) => v.includes(secret)),
		url: location.href.includes(secret),
		history: JSON.stringify(history.state ?? null).includes(secret),
		logs: logged.some((args) => args.some((a) => String(a).includes(secret)))
	};
}

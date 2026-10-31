import { isActionFailure, isRedirect, type ActionResult, type SubmitFunction } from '@sveltejs/kit';
import type { Session } from '$lib/server/auth/session';

/**
 * Test-only stand-in for `$app/forms` (installed for every test in `src/vitest.setup.ts`).
 * `enhance` runs the page's submit function and then the route's real form action (wired with
 * `useActions`), so component tests cover dialog + action + MSW together, without SvelteKit's
 * client runtime.
 */

type AnyAction = (event: never) => unknown;
type Actions = Record<string, AnyAction>;

interface Wiring {
	actions: Actions;
	params: Record<string, string>;
	session: Session | null;
	cookies: Record<string, string>;
}

const state: { wiring: Wiring | null; submissions: { action: string; form: FormData }[] } = {
	wiring: null,
	submissions: []
};

export const testSession: Session = {
	v: 1,
	provider: 'dev',
	user: { subject: 'dev|priya', name: 'Priya Sharma', email: null, roles: ['admin'], tenants: [] },
	accessToken: null,
	exp: Math.floor(Date.now() / 1000) + 3600
};

/** Routes `?/name` form posts to these actions (call in `beforeEach` of a page test). */
export function useActions(
	actions: Actions,
	opts: {
		params?: Record<string, string>;
		session?: Session | null;
		cookies?: Record<string, string>;
	} = {}
) {
	state.wiring = {
		actions,
		params: opts.params ?? {},
		session: opts.session === undefined ? testSession : opts.session,
		cookies: opts.cookies ?? {}
	};
	state.submissions = [];
}

/** Every form post seen by the fake `enhance` since `useActions`. */
export function submissions() {
	return state.submissions;
}

/** Calls a form action like SvelteKit would and maps its outcome to an `ActionResult`. */
export async function runAction(
	actions: Actions,
	name: string,
	form: FormData,
	opts: {
		params?: Record<string, string>;
		session?: Session | null;
		url?: string;
		cookies?: Record<string, string>;
	} = {}
): Promise<ActionResult> {
	const url = new URL(opts.url ?? 'http://console.test/page');
	const action = actions[name];
	if (!action) return { type: 'error', status: 404, error: new Error(`no action ${name}`) };
	const event = {
		request: new Request(`${url.origin}${url.pathname}?/${name}`, { method: 'POST', body: form }),
		params: opts.params ?? {},
		locals: { session: opts.session === undefined ? testSession : opts.session },
		url,
		fetch,
		cookies: { get: (k: string) => opts.cookies?.[k] }
	};
	try {
		const out = await action(event as never);
		if (isActionFailure(out))
			return {
				type: 'failure',
				status: out.status,
				data: out.data as unknown as Record<string, unknown>
			};
		return { type: 'success', status: 200, data: out as Record<string, unknown> };
	} catch (e) {
		if (isRedirect(e)) return { type: 'redirect', status: e.status, location: e.location };
		return { type: 'error', status: 500, error: e };
	}
}

export const invalidations: { count: number } = { count: 0 };

export function enhance(form: HTMLFormElement, submit?: SubmitFunction) {
	async function onsubmit(e: SubmitEvent) {
		e.preventDefault();
		const formData = new FormData(form);
		const raw = form.getAttribute('action') ?? '';
		const name = raw.startsWith('?/') ? raw.slice(2) : raw;
		const action = new URL(`http://console.test/page${raw.startsWith('?') ? raw : ''}`);
		let cancelled = false;
		const after = await submit?.({
			action,
			formData,
			formElement: form,
			controller: new AbortController(),
			submitter: e.submitter,
			cancel: () => (cancelled = true)
		});
		if (cancelled) return;
		state.submissions.push({ action: name, form: formData });
		const w = state.wiring;
		const result: ActionResult = w
			? await runAction(w.actions, name, formData, {
					params: w.params,
					session: w.session,
					cookies: w.cookies
				})
			: { type: 'error', status: 500, error: new Error('useActions() was not called') };
		if (typeof after === 'function') {
			await after({
				formData,
				formElement: form,
				action,
				result,
				update: async () => {
					invalidations.count++;
				}
			});
		}
	}
	form.addEventListener('submit', onsubmit);
	return {
		destroy() {
			form.removeEventListener('submit', onsubmit);
		}
	};
}

export async function applyAction(_result: ActionResult) {}
export function deserialize(text: string) {
	return JSON.parse(text);
}

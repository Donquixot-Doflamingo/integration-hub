import type { RequestEvent } from '@sveltejs/kit';
import { actionApi, finish, invalid, requireSession, text } from '$lib/server/actions';

/**
 * Transformer actions shared by `/transformers/{id}` (latest version) and
 * `/transformers/{id}/versions/{n}` (HUB-268, HUB-269). They are non-mutating POSTs that need the
 * `editor` role (`x-required-role`), so they run on the server like every other admin POST: the
 * bearer token never reaches the browser, and nothing they return is logged.
 */

/** Contract `TransformerRef`: `<transformer_id>@<version>`. */
const REF = /^[A-Za-z][A-Za-z0-9_-]{0,63}@[1-9][0-9]*$/;

export function readRef(op: string, form: FormData) {
	const ref = text(form, 'ref').trim();
	return REF.test(ref)
		? { ok: true as const, ref }
		: {
				ok: false as const,
				failure: invalid(op, { ref: 'Reload the page: the version is not known.' })
			};
}

/** `runTransformerGoldens` for a stored version: per-fixture pass/fail and diffs. */
export async function runGoldens(event: RequestEvent) {
	requireSession(event);
	const ref = readRef('runGoldens', await event.request.formData());
	if (!ref.ok) return ref.failure;
	const res = await actionApi(event).call((c) =>
		c.POST('/transformers/run-goldens', { body: { ref: ref.ref } })
	);
	return finish('runGoldens', res);
}

export const transformerActions = { runGoldens };

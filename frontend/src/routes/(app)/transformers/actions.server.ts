import type { RequestEvent } from '@sveltejs/kit';
import { DRY_RUN_FIELDS, dryRunBody } from '$lib/utils/dry-run';
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

/**
 * `dryRunTransformer` for a stored version: canonical input -> the exact vendor request, or vendor
 * response -> canonical response or error. Secrets come back redacted (`***`); the console shows
 * them as returned and never logs the result.
 */
export async function dryRun(event: RequestEvent) {
	requireSession(event);
	const form = await event.request.formData();
	const ref = readRef('dryRun', form);
	if (!ref.ok) return ref.failure;
	const parsed = dryRunBody(ref.ref, form);
	if (!parsed.ok) return invalid('dryRun', parsed.fields);
	const res = await actionApi(event).call((c) =>
		c.POST('/transformers/dry-run', { body: parsed.body })
	);
	return finish('dryRun', res, DRY_RUN_FIELDS);
}

export const transformerActions = { runGoldens, dryRun };

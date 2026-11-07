import type { RequestEvent } from '@sveltejs/kit';
import { DRY_RUN_FIELDS, dryRunBody } from '$lib/utils/dry-run';
import {
	actionApi,
	finish,
	finishByPointer,
	int,
	invalid,
	readReason,
	requireSession
} from '$lib/server/actions';
import { buildFixtures, createBody, draftOf, draftProblems, valuesFromForm } from './authoring';

/**
 * Transformer authoring actions (HUB-293), shared by `/transformers/new` and
 * `/transformers/{id}/new-version`. Goldens and dry runs of the unsaved draft are non-mutating
 * (editor); creating a version is the mutation (editor, reason, change id). The draft travels as
 * form fields with the fixtures as one JSON field, and is checked again here.
 */
const DRAFT_POINTERS = ['/kind', '/vendor_endpoint', '/yaml'] as const;

function readDraft(op: string, form: FormData) {
	const v = valuesFromForm(form);
	if (!v)
		return {
			ok: false as const,
			failure: invalid(op, { '/fixtures': 'Reload the page and try again.' })
		};
	const found = draftProblems(v);
	if (Object.keys(found).length) return { ok: false as const, failure: invalid(op, found) };
	return { ok: true as const, values: v };
}

/** runTransformerGoldens with `draft` + `fixtures`: per-fixture pass/fail and diffs. */
export async function runDraftGoldens(event: RequestEvent) {
	requireSession(event);
	const d = readDraft('runDraftGoldens', await event.request.formData());
	if (!d.ok) return d.failure;
	const fx = buildFixtures(d.values.fixtures, d.values.kind);
	if (!fx.ok) return invalid('runDraftGoldens', fx.fields);
	const res = await actionApi(event).call((c) =>
		c.POST('/transformers/run-goldens', {
			body: { draft: draftOf(d.values), fixtures: fx.fixtures }
		})
	);
	return finishByPointer('runDraftGoldens', res, [...DRAFT_POINTERS, '/fixtures']);
}

/** dryRunTransformer with `draft`: what the unsaved source produces (secrets come back `***`). */
export async function dryRunDraft(event: RequestEvent) {
	requireSession(event);
	const form = await event.request.formData();
	const d = readDraft('dryRunDraft', form);
	if (!d.ok) return d.failure;
	// The dry-run form fields are those of a stored version's dry run; only the source differs.
	const parsed = dryRunBody('draft@1', form);
	if (!parsed.ok) return invalid('dryRunDraft', parsed.fields);
	const { ref: _ref, ...rest } = parsed.body;
	const res = await actionApi(event).call((c) =>
		c.POST('/transformers/dry-run', { body: { ...rest, draft: draftOf(d.values) } })
	);
	return finish('dryRunDraft', res, DRY_RUN_FIELDS);
}

/** createTransformerVersion: `n+1` (expected_latest_version) or version 1 of a new id. */
export async function createVersion(event: RequestEvent) {
	requireSession(event);
	const form = await event.request.formData();
	const reason = readReason('createVersion', form);
	if (!reason.ok) return reason.failure;
	const v = valuesFromForm(form);
	if (!v) return invalid('createVersion', { '/fixtures': 'Reload the page and try again.' });
	const routeId = (event.params as { transformer_id?: string }).transformer_id;
	const mode = routeId ? 'version' : 'new';
	const id = routeId ?? v.transformer_id.trim();
	const expected = mode === 'version' ? int(form, 'expected_latest_version') : null;
	if (mode === 'version' && !expected)
		return invalid('createVersion', {
			'/expected_latest_version': 'Reload the page and try again.'
		});
	const built = createBody({ ...v, transformer_id: id }, mode, expected, reason.reason);
	if (!built.ok) return invalid('createVersion', built.fields);
	const res = await actionApi(event).call((c) =>
		c.POST('/transformers/{transformer_id}/versions', {
			params: { path: { transformer_id: id } },
			body: built.body
		})
	);
	return finishByPointer('createVersion', res, [
		'/transformer_id',
		...DRAFT_POINTERS,
		'/docs_url',
		'/fixtures',
		'/expected_latest_version'
	]);
}

export const authoringActions = { runDraftGoldens, dryRunDraft, createVersion };

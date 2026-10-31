import type { RequestEvent } from '@sveltejs/kit';
import type { DlqFilter } from '$lib/api/types';
import {
	actionApi,
	finish,
	int,
	invalid,
	readReason,
	requireSession,
	text
} from '$lib/server/actions';
import type { Actions } from './$types';
import { DRY_RUN_REASON, willAffect, type BulkKind } from './bulk';
import { filtersFromFields, toDlqFilter } from './filters';

// HUB-267: replay (`replayDeadLetters`, editor) and purge (`purgeDeadLetters`, admin) by the list's
// filters, run on the server. Each has a count step (dry run) and the real call.

function call(
	event: RequestEvent,
	kind: BulkKind,
	filter: DlqFilter,
	reason: string,
	dry: boolean
) {
	const body = { filter, dry_run: dry, reason };
	const api = actionApi(event);
	return kind === 'replay'
		? api.call((c) => c.POST('/dlq/replay', { body }))
		: api.call((c) => c.POST('/dlq/purge', { body }));
}

/** Dry run: the exact number of entries the current filters match. */
async function count(event: RequestEvent, kind: BulkKind) {
	requireSession(event);
	const op = kind === 'replay' ? 'countReplay' : 'countPurge';
	const filter = toDlqFilter(filtersFromFields(await event.request.formData()));
	const res = await call(event, kind, filter, DRY_RUN_REASON, true);
	return finish(op, res);
}

async function run(event: RequestEvent, kind: BulkKind) {
	requireSession(event);
	const form = await event.request.formData();
	const reason = readReason(kind, form);
	if (!reason.ok) return reason.failure;
	const expected = int(form, 'expected');
	if (expected === null || expected < 1) {
		return invalid(kind, { reason: 'Count the matching dead letters first.' });
	}
	const filter = toDlqFilter(filtersFromFields(form));
	if (kind === 'purge') {
		// Typed confirmation (F-19 rule 6): the count, checked again here.
		if (text(form, 'confirm').trim() !== String(expected)) {
			return invalid(kind, { confirm: `Type ${expected} to confirm.` });
		}
		// Purge is permanent: refuse when the selection changed since it was counted.
		const now = await call(event, kind, filter, DRY_RUN_REASON, true);
		if (!now.ok) return finish(kind, now);
		if (willAffect(now.data) !== expected) {
			return invalid(kind, {
				confirm: `The selection changed: ${now.data.matched} now match, not ${expected}. Cancel and count again.`
			});
		}
	}
	const res = await call(event, kind, filter, reason.reason, false);
	return finish(kind, res);
}

export const actions = {
	countReplay: (event) => count(event, 'replay'),
	countPurge: (event) => count(event, 'purge'),
	replay: (event) => run(event, 'replay'),
	purge: (event) => run(event, 'purge')
} satisfies Actions;

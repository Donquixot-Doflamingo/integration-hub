import {
	actionApi,
	finishByPointer,
	invalid,
	readReason,
	requireSession,
	text
} from '$lib/server/actions';
import type { Actions } from './$types';
import { POINTERS, updateBody, valuesFromForm, type FormValues } from '../../vendor-form';

// HUB-290: replace a vendor's mutable fields (updateVendor, admin, PUT). The ETag the page loaded
// goes back as If-Match, so a concurrent edit answers 412 PRECONDITION_FAILED (the form offers a
// reload) instead of being overwritten.
export const actions = {
	update: async (event) => {
		requireSession(event);
		const form = await event.request.formData();
		const reason = readReason('update', form);
		if (!reason.ok) return reason.failure;
		const before = beforeFrom(form);
		if (!before) return invalid('update', { '/display_name': 'Reload the page and try again.' });
		const built = updateBody(valuesFromForm(form), before, reason.reason);
		if (!built.ok) return invalid('update', built.fields);
		const etag = text(form, 'if_match').trim();
		const res = await actionApi(event).call((c) =>
			c.PUT('/vendors/{vendor_id}', {
				params: {
					path: { vendor_id: event.params.vendor_id },
					header: etag ? { 'If-Match': etag } : {}
				},
				body: built.body
			})
		);
		return finishByPointer('update', res, POINTERS);
	}
} satisfies Actions;

/** The values the page opened with (hidden `before`), to refuse an edit that changes nothing. */
function beforeFrom(form: FormData): FormValues | null {
	try {
		const v = JSON.parse(text(form, 'before')) as FormValues;
		return v && typeof v === 'object' && Array.isArray(v.regions) ? v : null;
	} catch {
		return null;
	}
}

import type { FieldErrors } from '$lib/api/actions';
import type { TransformerDryRunRequest } from '$lib/api/types';
import { parseJsonObject } from './json-input';

/**
 * Transformer dry-run form -> `TransformerDryRunRequest` (HUB-269). Pure, so the page checks the
 * form in the browser before submitting (invalid JSON never leaves it) and the form action runs
 * the same checks again (no-JS path). Field names are the request's property names.
 */

/** Contract `Slug`. */
const SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/;

type VendorResponse = NonNullable<TransformerDryRunRequest['vendor_response']>;

/** `{ status?, headers?, body? }` as the contract's `vendor_response` takes it. */
export function vendorResponse(
	raw: string
): { ok: true; value: VendorResponse } | { ok: false; message: string } {
	const r = parseJsonObject(raw, { what: 'The vendor response' });
	if (!r.ok) return r;
	const v = r.value!;
	if (v.status !== undefined && !(Number.isInteger(v.status) && (v.status as number) >= 100)) {
		return { ok: false, message: 'The vendor response: "status" must be an HTTP status code.' };
	}
	if (v.headers !== undefined) {
		const h = parseJsonObject(JSON.stringify(v.headers), {
			stringValues: true,
			what: 'The vendor response headers'
		});
		if (!h.ok) return h;
	}
	return { ok: true, value: v as VendorResponse };
}

/** Form fields of the dry-run form (also the names the page marks invalid). */
export const DRY_RUN_FIELDS = ['account_id', 'input', 'headers', 'vendor_response'] as const;

/**
 * The dry-run request from the form, or field errors. `mode` says which body the transformer
 * takes: a canonical `input` (request and inbound transformers) or a `vendor_response`.
 */
export function dryRunBody(
	ref: string,
	form: FormData
): { ok: true; body: TransformerDryRunRequest } | { ok: false; fields: FieldErrors } {
	const fields: FieldErrors = {};
	const body: TransformerDryRunRequest = { ref };
	const account = field(form, 'account_id').trim();
	if (account) {
		if (SLUG.test(account)) body.account_id = account;
		else fields.account_id = 'Use an account id such as ct-brandA-in.';
	}
	if (field(form, 'mode') === 'response') {
		const vr = vendorResponse(field(form, 'vendor_response'));
		if (vr.ok) body.vendor_response = vr.value;
		else fields.vendor_response = vr.message;
	} else {
		const input = parseJsonObject(field(form, 'input'), { what: 'The input' });
		if (input.ok) body.input = input.value;
		else fields.input = input.message;
		const headers = parseJsonObject(field(form, 'headers'), {
			optional: true,
			stringValues: true,
			what: 'The caller headers'
		});
		if (!headers.ok) fields.headers = headers.message;
		else if (headers.value) body.headers = headers.value as Record<string, string>;
	}
	return Object.keys(fields).length ? { ok: false, fields } : { ok: true, body };
}

function field(form: FormData, name: string): string {
	const v = form.get(name);
	return typeof v === 'string' ? v : '';
}

import type { Operations } from '$lib/api/types';

/** `weights` of `updateIntegrationWeights` (targets matched by vendor_id + account_id). */
export type WeightsBody =
	Operations['updateIntegrationWeights']['requestBody']['content']['application/json']['weights'];

/**
 * Why these weights cannot be submitted, or null (HUB-260). `values` are the parsed inputs (NaN
 * for anything that is not a whole number); `current` the active version's weights, same order.
 */
export function weightsProblem(values: number[], current: number[] = []): string | null {
	if (values.length === 0) return 'The active version has no targets.';
	if (values.some((v) => !Number.isInteger(v) || v < 0 || v > 100))
		return 'Each weight is a whole number from 0 to 100.';
	const total = values.reduce((a, b) => a + b, 0);
	if (total !== 100) return `Weights must total 100 (now ${total}).`;
	if (current.length === values.length && values.every((v, i) => v === current[i]))
		return 'Change at least one weight; these are the current weights.';
	return null;
}

/** Reads `targets`, `vendor_<n>`, `account_<n>`, `weight_<n>` from the weights form. */
export function weightsFromForm(
	form: FormData
): { ok: true; weights: WeightsBody } | { ok: false; message: string } {
	const count = Number(form.get('targets'));
	if (!Number.isInteger(count) || count < 1 || count > 100)
		return { ok: false, message: 'Reload the page: the targets are not known.' };
	const weights: WeightsBody = [];
	for (let n = 0; n < count; n++) {
		const vendor_id = String(form.get(`vendor_${n}`) ?? '');
		const account_id = String(form.get(`account_${n}`) ?? '');
		const raw = String(form.get(`weight_${n}`) ?? '').trim();
		if (!vendor_id || !account_id)
			return { ok: false, message: 'Reload the page: a target is missing.' };
		weights.push({ vendor_id, account_id, weight: /^\d{1,3}$/.test(raw) ? Number(raw) : NaN });
	}
	const problem = weightsProblem(weights.map((w) => w.weight));
	return problem ? { ok: false, message: problem } : { ok: true, weights };
}

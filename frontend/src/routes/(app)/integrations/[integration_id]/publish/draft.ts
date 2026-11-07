import type { PlainApiError } from '$lib/api/actions';
import type { ValidationCheck, ValidationReport } from '$lib/api/types';

/** Result of the `saveDraft` action (HUB-259). */
export interface SavedDraft {
	version: number;
	/** Publish checks run right after saving; null when the check call itself failed. */
	report: ValidationReport | null;
}

/** A problem to show on the spec editor: JSON pointer into the spec + message. */
export interface SpecProblem {
	pointer: string;
	message: string;
}

/**
 * Problems for the spec editor from an action error. `VALIDATION_FAILED` pointers are relative to
 * the request body (`/spec/targets/0/weight`); `PUBLISH_VALIDATION_FAILED` pointers are relative
 * to the spec (`/targets/1/account_id`). Body pointers outside `/spec` (e.g. `/reason`) are not
 * spec problems.
 */
export function specProblems(error: PlainApiError | null | undefined): SpecProblem[] {
	if (!error) return [];
	const out: SpecProblem[] = [];
	for (const d of error.details) {
		if (!d.pointer) continue;
		if (error.code === 'PUBLISH_VALIDATION_FAILED')
			out.push({ pointer: d.pointer, message: d.message });
		else if (d.pointer === '/spec' || d.pointer.startsWith('/spec/'))
			out.push({ pointer: d.pointer.slice('/spec'.length) || '/', message: d.message });
	}
	return out;
}

/** The report in `error.meta.report` of a 422 PUBLISH_VALIDATION_FAILED, if any. */
export function reportFromError(error: PlainApiError | null | undefined): ValidationReport | null {
	const report = (error?.meta as { report?: unknown } | null | undefined)?.report;
	if (!report || typeof report !== 'object' || !Array.isArray((report as ValidationReport).checks))
		return null;
	return report as ValidationReport;
}

/** Failing checks of a report as spec problems (checks carry a pointer into the spec). */
export function reportProblems(report: ValidationReport | null): SpecProblem[] {
	return (report?.checks ?? [])
		.filter((c: ValidationCheck) => c.status === 'fail' && c.pointer)
		.map((c) => ({ pointer: c.pointer!, message: c.message ?? c.check }));
}

export const checkLabels: Record<string, string> = {
	schemas_compile: 'Schemas compile',
	expressions_typecheck: 'Expressions type-check',
	goldens_pass: 'Samples produce the expected output',
	input_schema: 'Sample inputs fit the request schema',
	endpoint_schema: 'Produced requests fit the vendor endpoint schema',
	references_exist: 'Referenced vendors, accounts and transformers exist',
	accounts_not_retired: 'No retired accounts',
	interactive_sync_only: 'Interactive integrations use sync',
	weights_sum_100: 'Weights total 100',
	delivery_path_declared: 'Delivery path declared',
	failover_requires_idempotency: 'Failover has idempotency',
	supports_features: 'Vendors support the features used',
	operations_have_transformers: 'Every operation has a transformer'
};

/**
 * Label of a publish check. The contract says clients must tolerate unknown check names, so a
 * name without a label reads as a sentence ("new_check_name" -> "New check name") instead of a
 * raw identifier (HUB-289: `operations_have_transformers`, added in hub-admin 1.2.0, showed raw).
 */
export function checkLabel(check: string): string {
	const known = checkLabels[check];
	if (known) return known;
	const words = check.replace(/[_-]+/g, ' ').trim();
	return words ? words[0]!.toUpperCase() + words.slice(1) : check;
}

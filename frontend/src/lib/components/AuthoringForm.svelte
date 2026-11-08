<script lang="ts">
	import type { Snippet } from 'svelte';
	import { tick } from 'svelte';
	import type { SubmitFunction } from '@sveltejs/kit';
	import { applyAction, enhance } from '$app/forms';
	import { invalidateAll } from '$app/navigation';
	import {
		fromPlainError,
		isConcurrencyError,
		type ActionFailureData,
		type ActionSuccessData,
		type FieldErrors
	} from '$lib/api/actions';
	import { HubApiError } from '$lib/api/errors';
	import { REASON_MAX, REASON_MIN, reasonProblem } from '$lib/reason';
	import ErrorPanel from './ErrorPanel.svelte';
	import FieldError from './FieldError.svelte';
	import FormProblems from './FormProblems.svelte';
	import Icon from './Icon.svelte';

	/**
	 * Full-page create / edit form (vendors, accounts, integrations, transformers; HUB-290+). It
	 * posts to a SvelteKit form action with `use:enhance` (the admin token stays on the server),
	 * with the mandatory change reason at the end, and:
	 * - refuses to send while the console's own checks fail (`blockers`): the submit button stays
	 *   usable (a disabled button explains nothing), and pressing it lists the problems and moves
	 *   focus to the first invalid field;
	 * - on failure keeps the input, puts API pointers on their fields (`fields` snippet gets them)
	 *   and shows code, message and request id; a 412 / stale 409 offers a reload;
	 * - on success hands the action data (with the change log id) to `onsuccess`.
	 * `onsubmitted` runs right after the browser captured the form data, before the request goes
	 * out: secret inputs are cleared there (HUB-291), whatever the outcome.
	 */
	let {
		action,
		verb,
		busyVerb,
		subject,
		reasonLabel = 'Reason',
		reasonHint,
		blockers = [],
		conflictIsStale = true,
		children,
		onsubmitted,
		onsuccess,
		onfailure
	}: {
		action: string;
		/** Submit label, the action's verb: "Create vendor". */
		verb: string;
		busyVerb: string;
		/** What may have changed under the operator, for the reload prompt. */
		subject: string;
		reasonLabel?: string;
		reasonHint?: string;
		/** Problems the console found; nothing is sent while there are any. */
		blockers?: { label?: string; message: string }[];
		/** See ConfirmDialog: false when 409 means "already exists" rather than a race. */
		conflictIsStale?: boolean;
		/** The inputs; gets the action's field errors and whether a submit was attempted. */
		children: Snippet<[FieldErrors, boolean]>;
		onsubmitted?: () => void;
		onsuccess?: (data: ActionSuccessData) => void | Promise<void>;
		onfailure?: (data: ActionFailureData) => void;
	} = $props();

	const id = $props.id();
	let reason = $state('');
	let reasonTouched = $state(false);
	let attempted = $state(false);
	let submitting = $state(false);
	let error = $state.raw<HubApiError | null>(null);
	let fields = $state.raw<FieldErrors>({});
	let form = $state<HTMLFormElement>();

	const problem = $derived(reasonProblem(reason));
	const reasonError = $derived(
		fields['/reason'] ?? fields.reason ?? ((reasonTouched || attempted) && problem ? problem : null)
	);
	const conflict = $derived(
		error && isConcurrencyError(error) && (conflictIsStale || error.code !== 'CONFLICT')
			? error
			: null
	);
	const allBlockers = $derived(
		problem && attempted ? [...blockers, { label: reasonLabel, message: problem }] : blockers
	);

	const submit: SubmitFunction = ({ cancel }) => {
		if (submitting) return cancel();
		attempted = true;
		if (blockers.length || problem) {
			cancel();
			tick().then(() => form?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus());
			return;
		}
		submitting = true;
		error = null;
		fields = {};
		onsubmitted?.();
		return async ({ result }) => {
			try {
				if (result.type === 'success') {
					reason = '';
					attempted = false;
					reasonTouched = false;
					await onsuccess?.(result.data as ActionSuccessData);
				} else if (result.type === 'failure') {
					const d = result.data as ActionFailureData | undefined;
					error = d?.error ? fromPlainError(d.error) : null;
					fields = d?.fields ?? {};
					if (d) onfailure?.(d);
					await tick();
					form
						?.querySelector<HTMLElement>('[aria-invalid="true"], .fk-summary, [role="alert"]')
						?.focus?.();
				} else if (result.type === 'redirect') {
					await applyAction(result);
				} else {
					error = new HubApiError({
						status: result.type === 'error' ? (result.status ?? 500) : 500,
						code: 'UNEXPECTED_RESPONSE',
						message: 'The console could not complete this action. Try again.'
					});
				}
			} finally {
				submitting = false;
			}
		};
	};
</script>

<form bind:this={form} class="fk-form" method="POST" {action} use:enhance={submit} novalidate>
	{@render children(fields, attempted)}

	<div class="fk-group">
		<div class="fk-field">
			<label for="{id}-reason">{reasonLabel} <span class="fk-req">(required)</span></label>
			<textarea
				id="{id}-reason"
				name="reason"
				rows="2"
				maxlength={REASON_MAX}
				bind:value={reason}
				onblur={() => (reasonTouched = true)}
				disabled={submitting}
				aria-invalid={reasonError ? 'true' : undefined}
				aria-describedby="{id}-reason-hint{reasonError ? ` ${id}-reason-error` : ''}"></textarea>
			<p id="{id}-reason-hint" class="fk-hint">
				{reasonHint ??
					`At least ${REASON_MIN} characters. Saved in the change log with your name; cite a ticket if there is one.`}
			</p>
			<FieldError id="{id}-reason-error" message={reasonError} />
		</div>
	</div>

	{#if attempted}<FormProblems items={allBlockers} />{/if}

	{#if conflict}
		<div class="conflict" role="alert" tabindex="-1">
			<p><strong>Someone else changed {subject}.</strong> {conflict.message}</p>
			<p>Reload to see the current state. Copy your changes first if you want to keep them.</p>
			<button type="button" class="btn" onclick={() => invalidateAll()}>
				<Icon name="retry" size={16} />Reload
			</button>
		</div>
	{:else if error}
		<ErrorPanel {error} context="Could not {verb.toLowerCase()}" headingLevel={2} />
	{/if}

	<div class="fk-actions">
		<button type="submit" class="btn btn-primary" disabled={submitting}>
			{submitting ? busyVerb : verb}
		</button>
		{#if submitting}<span class="visually-hidden" role="status">{busyVerb}</span>{/if}
	</div>
</form>

<style>
	.conflict {
		padding: var(--space-3);
		border: 1px solid var(--status-warn);
		border-left-width: 4px;
		border-radius: var(--radius-md);
		background: var(--status-warn-wash);
		font-size: var(--text-sm);
	}
	.conflict p {
		margin: 0 0 var(--space-2);
	}
</style>

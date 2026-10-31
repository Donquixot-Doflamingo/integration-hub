<script lang="ts">
	import { tick, untrack, type Snippet } from 'svelte';
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
	import Icon from './Icon.svelte';

	/**
	 * The one confirm dialog for every dangerous action (admin-console-ux §1): exact title, plain
	 * consequence, mandatory reason (sent to the API, lands in change_log), optional typed
	 * confirmation, a confirm button that repeats the verb, Cancel focused first, no double submit.
	 * It posts to a SvelteKit form action with `use:enhance`. On success it closes and hands the
	 * action's data (with `changeId`) to `onsuccess`; on failure it stays open with the reason
	 * intact and shows the error code, message and request id, or a reload prompt when someone
	 * else changed the object first (409 CONFLICT / 412 PRECONDITION_FAILED).
	 */
	let {
		open = $bindable(false),
		title,
		verb,
		busyVerb,
		action,
		tone = 'danger',
		confirmText,
		ready = true,
		subject,
		children,
		fields,
		extra,
		oneTimeSecret = false,
		onsuccess,
		onfailure
	}: {
		open?: boolean;
		/** "Roll back int_evt_brandA to v7". */
		title: string;
		/** Confirm button label, the action's verb: "Roll back". Never "OK". */
		verb: string;
		/** While submitting: "Rolling back...". */
		busyVerb: string;
		/** Form action, e.g. `?/rollback`. */
		action: string;
		tone?: 'danger' | 'primary';
		/** When set, the operator must type this exact text (e.g. an account id) to confirm. */
		confirmText?: string;
		/** Extra validity from `fields` (e.g. weights total 100). */
		ready?: boolean;
		/** What may have changed under the operator, for the reload prompt: "int_evt_brandA". */
		subject: string;
		/** The consequence, in plain words with numbers. */
		children: Snippet;
		/** Inputs that go with the action (hidden ids, weights, duration). Gets field errors. */
		fields?: Snippet<[FieldErrors]>;
		/** Shown under the error, e.g. the failing publish checks. */
		extra?: Snippet;
		/**
		 * The success answer carries a secret shown once (a new API key, HUB-271). It goes to
		 * `onsuccess` only: the page data is reloaded with `invalidateAll` instead of `update`, so the
		 * result is never applied as the page's `form` (where it would outlive the reveal).
		 */
		oneTimeSecret?: boolean;
		onsuccess?: (data: ActionSuccessData) => void;
		/** The action's failure data, e.g. to mark publish problems on the spec editor. */
		onfailure?: (data: ActionFailureData) => void;
	} = $props();

	const id = $props.id();
	let reason = $state('');
	let typed = $state('');
	let touched = $state(false);
	let submitting = $state(false);
	let error = $state.raw<HubApiError | null>(null);
	let fieldErrors = $state.raw<FieldErrors>({});
	let dialog = $state<HTMLElement>();
	let cancelButton = $state<HTMLButtonElement>();

	const problem = $derived(reasonProblem(reason));
	const typedOk = $derived(!confirmText || typed.trim() === confirmText);
	const valid = $derived(!problem && typedOk && ready);
	const reasonError = $derived(fieldErrors.reason ?? (touched && reason ? problem : null));
	const conflict = $derived(error && isConcurrencyError(error) ? error : null);

	// Open: reset, remember the trigger, focus Cancel. Close: give focus back to the trigger.
	$effect(() => {
		if (!open) return;
		const trigger = untrack(() => document.activeElement) as HTMLElement | null;
		untrack(() => {
			reason = '';
			typed = '';
			touched = false;
			error = null;
			fieldErrors = {};
			submitting = false;
		});
		tick().then(() => cancelButton?.focus());
		return () => {
			const target = trigger?.isConnected ? trigger : document.getElementById('main');
			target?.focus();
		};
	});

	function close() {
		if (!submitting) open = false;
	}

	const focusable =
		'button:not([disabled]), [href], input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') {
			e.preventDefault();
			e.stopPropagation();
			close();
			return;
		}
		if (e.key !== 'Tab' || !dialog) return;
		const items = [...dialog.querySelectorAll<HTMLElement>(focusable)];
		const first = items[0];
		const last = items[items.length - 1];
		if (!first || !last) return;
		if (e.shiftKey && document.activeElement === first) {
			e.preventDefault();
			last.focus();
		} else if (!e.shiftKey && document.activeElement === last) {
			e.preventDefault();
			first.focus();
		}
	}

	async function reload() {
		open = false;
		await invalidateAll();
	}

	const submit: SubmitFunction = ({ cancel }) => {
		if (submitting || !valid) {
			touched = true;
			cancel();
			return;
		}
		submitting = true;
		error = null;
		fieldErrors = {};
		return async ({ result, update }) => {
			try {
				if (result.type === 'success') {
					open = false;
					onsuccess?.(result.data as ActionSuccessData);
					// Reload the page data so the new state (active version, pause) shows.
					if (oneTimeSecret) await invalidateAll();
					else await update({ reset: false });
				} else if (result.type === 'failure') {
					const data = result.data as ActionFailureData | undefined;
					error = data?.error ? fromPlainError(data.error) : null;
					fieldErrors = data?.fields ?? {};
					if (data) onfailure?.(data);
				} else if (result.type === 'redirect') {
					open = false;
					await applyAction(result);
				} else {
					error = new HubApiError({
						status: result.status ?? 500,
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

{#if open}
	<div class="backdrop">
		<div
			bind:this={dialog}
			class="dialog {tone}"
			role="dialog"
			aria-modal="true"
			aria-labelledby="{id}-title"
			aria-describedby="{id}-body"
			tabindex="-1"
			{onkeydown}
		>
			<h2 id="{id}-title">{title}</h2>
			<div id="{id}-body" class="consequence">{@render children()}</div>

			<form method="POST" {action} use:enhance={submit} novalidate>
				{@render fields?.(fieldErrors)}

				<div class="field">
					<label for="{id}-reason">Reason <span class="req">(required)</span></label>
					<textarea
						id="{id}-reason"
						name="reason"
						rows="3"
						maxlength={REASON_MAX}
						required
						bind:value={reason}
						onblur={() => (touched = true)}
						aria-invalid={reasonError ? 'true' : undefined}
						aria-describedby="{id}-reason-hint{reasonError ? ` ${id}-reason-error` : ''}"
						disabled={submitting}></textarea>
					<p id="{id}-reason-hint" class="hint">
						At least {REASON_MIN} characters. Saved in the change log with your name; cite a ticket if
						there is one.
					</p>
					{#if reasonError}
						<p id="{id}-reason-error" class="field-error">
							<Icon name="alert" size={14} />{reasonError}
						</p>
					{/if}
				</div>

				{#if confirmText}
					<div class="field">
						<label for="{id}-confirm">Type <code>{confirmText}</code> to confirm</label>
						<input
							id="{id}-confirm"
							name="confirm"
							autocomplete="off"
							spellcheck="false"
							bind:value={typed}
							aria-invalid={fieldErrors.confirm ? 'true' : undefined}
							aria-describedby={fieldErrors.confirm ? `${id}-confirm-error` : undefined}
							disabled={submitting}
						/>
						{#if fieldErrors.confirm}
							<p id="{id}-confirm-error" class="field-error">
								<Icon name="alert" size={14} />{fieldErrors.confirm}
							</p>
						{/if}
					</div>
				{/if}

				{#if conflict}
					<div class="conflict" role="alert">
						<p class="conflict-title">
							<Icon name="alert" size={16} />Someone else changed {subject}
						</p>
						<p>
							{conflict.message.replace(/\.$/, '')}. Reload to see the current state, check it, then
							try again. Your reason is kept until you close this dialog.
						</p>
						<p class="rid">
							Code <code>{conflict.code}</code>{#if conflict.requestId}, request
								<code class="id">{conflict.requestId}</code>{/if}
						</p>
						<button type="button" class="reload" onclick={reload}>
							<Icon name="retry" size={16} />Reload
						</button>
					</div>
				{:else if error}
					<ErrorPanel {error} context="Could not {verb.toLowerCase()}" headingLevel={3} />
				{/if}
				{#if error}{@render extra?.()}{/if}

				<div class="buttons">
					<button
						bind:this={cancelButton}
						type="button"
						class="cancel"
						onclick={close}
						disabled={submitting}>Cancel</button
					>
					<button type="submit" class="confirm" disabled={!valid || submitting}>
						{submitting ? busyVerb : verb}
					</button>
				</div>
				{#if submitting}<p class="visually-hidden" role="status">{busyVerb}</p>{/if}
			</form>
		</div>
	</div>
{/if}

<style>
	.backdrop {
		position: fixed;
		inset: 0;
		z-index: 50;
		display: grid;
		place-items: start center;
		padding: 8vh var(--space-4) var(--space-4);
		overflow-y: auto;
		background: rgb(10 19 29 / 0.55);
	}
	.dialog {
		width: min(40rem, 100%);
		padding: var(--space-5);
		border: 1px solid var(--rule-strong);
		border-top: 4px solid var(--signal);
		border-radius: var(--radius-md);
		background: var(--surface);
		color: var(--ink);
		box-shadow: 0 18px 48px rgb(10 19 29 / 0.35);
	}
	.dialog.danger {
		border-top-color: var(--status-danger);
	}
	h2 {
		font-size: var(--text-lg);
	}
	.consequence {
		margin: var(--space-2) 0 var(--space-4);
		font-size: var(--text-sm);
	}
	.consequence :global(p) {
		margin: 0 0 var(--space-2);
	}
	form {
		display: grid;
		gap: var(--space-3);
	}
	.field {
		display: grid;
		gap: 0.25rem;
	}
	label {
		font-weight: 600;
		font-size: var(--text-sm);
	}
	.req {
		color: var(--ink-muted);
		font-weight: 400;
	}
	textarea,
	input {
		width: 100%;
		padding: 0.4rem 0.55rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-sm);
	}
	textarea {
		resize: vertical;
		min-height: 4.5rem;
	}
	[aria-invalid='true'] {
		border-color: var(--status-danger);
	}
	.hint {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.field-error {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		margin: 0;
		color: var(--status-danger);
		font-size: var(--text-sm);
		font-weight: 600;
	}
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
	.conflict-title {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		font-weight: 700;
	}
	.rid {
		color: var(--ink-muted);
	}
	.buttons {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-2);
		margin-top: var(--space-2);
	}
	.buttons button,
	.reload {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		min-height: 2.25rem;
		padding: 0 1rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-weight: 700;
		cursor: pointer;
	}
	.buttons .confirm {
		border-color: var(--signal);
		background: var(--signal);
		color: var(--surface);
	}
	.danger .buttons .confirm {
		border-color: var(--status-danger);
		background: var(--status-danger);
	}
	button:disabled {
		cursor: not-allowed;
		opacity: 0.55;
	}
</style>

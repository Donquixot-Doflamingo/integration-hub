<script lang="ts">
	import { tick } from 'svelte';
	import type { SubmitFunction } from '@sveltejs/kit';
	import { enhance } from '$app/forms';
	import {
		fromPlainError,
		type ActionFailureData,
		type ActionSuccessData,
		type FieldErrors
	} from '$lib/api/actions';
	import { HubApiError } from '$lib/api/errors';
	import type { TransformerDryRunResult, TransformerVersion } from '$lib/api/types';
	import { dryRunBody } from '$lib/utils/dry-run';
	import { prettyJson } from '$lib/utils/json-input';
	import ErrorPanel from '../ErrorPanel.svelte';
	import Icon from '../Icon.svelte';
	import DryRunResult from './DryRunResult.svelte';

	/**
	 * Dry-run one stored version (HUB-269, `dryRunTransformer`, editor): paste a caller body (or,
	 * for a response transformer, a vendor response) and see what the transformer would produce.
	 * JSON is checked in the browser first, so invalid input is caught before anything is sent;
	 * the form action checks it again. A fixture can be loaded as a starting point. The result shows
	 * secrets as the API redacted them (`***`) and is kept only in this page's memory.
	 */
	let { version: v }: { version: TransformerVersion } = $props();

	const uid = $props.id();
	const mode = $derived(v.kind === 'response' ? 'response' : 'request');
	const sample = (name: string) => {
		const f = v.fixtures.find((x) => x.name === name);
		return mode === 'response' ? f?.vendor_response : f?.input;
	};
	const withSample = $derived(v.fixtures.filter((f) => sample(f.name) !== undefined));

	let body = $state('');
	let headers = $state('');
	let account = $state('');
	let fixture = $state('');
	let running = $state(false);
	let fields = $state.raw<FieldErrors>({});
	let error = $state.raw<HubApiError | null>(null);
	let result = $state.raw<TransformerDryRunResult | null>(null);
	let form = $state<HTMLFormElement>();

	// Start from the first fixture that has a body (the operator can edit or replace it).
	$effect.pre(() => {
		const first = withSample[0];
		body = first ? prettyJson(sample(first.name)) : '';
		fixture = first?.name ?? '';
	});

	function loadFixture() {
		const value = sample(fixture);
		if (value !== undefined) body = prettyJson(value);
		fields = {};
	}

	const bodyName = $derived(mode === 'response' ? 'vendor_response' : 'input');
	const bodyLabel = $derived(
		mode === 'response'
			? 'Vendor response (JSON: status, headers, body)'
			: 'Caller input (JSON body)'
	);

	// Checked on submit only: checking on blur inserts the error line and moves the submit button
	// between mousedown and mouseup, which loses the click.
	function check(): boolean {
		if (!form) return false;
		const parsed = dryRunBody(v.ref, new FormData(form));
		fields = parsed.ok ? {} : parsed.fields;
		return parsed.ok;
	}

	const submit: SubmitFunction = ({ cancel }) => {
		if (running) return cancel();
		// Invalid JSON never leaves the browser.
		if (!check()) {
			cancel();
			// Move focus to the first problem once it is marked.
			tick().then(() => form?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus());
			return;
		}
		running = true;
		error = null;
		return async ({ result: r }) => {
			try {
				if (r.type === 'success') {
					result = (r.data as ActionSuccessData<TransformerDryRunResult>).result;
				} else if (r.type === 'failure') {
					const d = r.data as ActionFailureData | undefined;
					fields = d?.fields ?? {};
					error = d?.error ? fromPlainError(d.error) : null;
					result = null;
				} else {
					error = new HubApiError({
						status: r.type === 'error' ? (r.status ?? 500) : 500,
						code: 'UNEXPECTED_RESPONSE',
						message: 'The console could not run the dry run. Try again.'
					});
				}
			} finally {
				running = false;
			}
		};
	};
</script>

<form
	bind:this={form}
	method="POST"
	action="?/dryRun"
	use:enhance={submit}
	novalidate
	class="dry-run"
	aria-labelledby="{uid}-title"
>
	<h4 id="{uid}-title" class="visually-hidden">Dry-run {v.ref}</h4>
	<input type="hidden" name="ref" value={v.ref} />
	<input type="hidden" name="mode" value={mode} />
	<div class="row">
		{#if withSample.length}
			<div class="field">
				<label for="{uid}-fixture">Start from fixture</label>
				<div class="inline">
					<select id="{uid}-fixture" bind:value={fixture}>
						{#each withSample as f (f.name)}<option value={f.name}>{f.name}</option>{/each}
					</select>
					<button type="button" class="secondary" onclick={loadFixture}>Load</button>
				</div>
			</div>
		{/if}
		<div class="field">
			<label for="{uid}-account">Account <span class="hint">(optional)</span></label>
			<input
				id="{uid}-account"
				name="account_id"
				bind:value={account}
				maxlength="64"
				placeholder="ct-brandA-in"
				autocomplete="off"
				aria-invalid={fields.account_id ? 'true' : undefined}
				aria-describedby="{uid}-account-hint{fields.account_id ? ` ${uid}-account-error` : ''}"
			/>
			<p id="{uid}-account-hint" class="hint">
				Its non-secret config is injected; credentials come back as <code>***</code>.
			</p>
			{#if fields.account_id}
				<p id="{uid}-account-error" class="field-error">
					<Icon name="alert" size={14} />{fields.account_id}
				</p>
			{/if}
		</div>
	</div>
	<div class="field">
		<label for="{uid}-body">{bodyLabel} <span class="hint">(required)</span></label>
		<textarea
			id="{uid}-body"
			name={bodyName}
			rows="10"
			spellcheck="false"
			bind:value={body}
			aria-invalid={fields[bodyName] ? 'true' : undefined}
			aria-describedby={fields[bodyName] ? `${uid}-body-error` : undefined}></textarea>
		{#if fields[bodyName]}
			<p id="{uid}-body-error" class="field-error">
				<Icon name="alert" size={14} />{fields[bodyName]}
			</p>
		{/if}
	</div>
	{#if mode === 'request'}
		<div class="field">
			<label for="{uid}-headers"
				>Caller headers <span class="hint">(optional JSON object of strings)</span></label
			>
			<textarea
				id="{uid}-headers"
				name="input_headers"
				rows="3"
				spellcheck="false"
				bind:value={headers}
				placeholder={'{ "X-Request-Source": "checkout" }'}
				aria-invalid={fields.input_headers ? 'true' : undefined}
				aria-describedby={fields.input_headers ? `${uid}-headers-error` : undefined}></textarea>
			{#if fields.input_headers}
				<p id="{uid}-headers-error" class="field-error">
					<Icon name="alert" size={14} />{fields.input_headers}
				</p>
			{/if}
		</div>
	{/if}
	<div class="buttons">
		<button type="submit" aria-disabled={running ? 'true' : undefined}>
			{running ? 'Running...' : `Dry-run ${v.ref}`}
		</button>
		<span class="hint">Non-mutating: nothing is sent to the vendor or saved.</span>
	</div>
	{#if running}<p class="visually-hidden" role="status">Running the dry run</p>{/if}
</form>

{#if error}
	<ErrorPanel {error} context="Could not run the dry run" headingLevel={3} />
{:else if result}
	<DryRunResult {result} refLabel={v.ref} />
{/if}

<style>
	.dry-run {
		display: grid;
		gap: var(--space-3);
		margin-bottom: var(--space-3);
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-4);
	}
	.field {
		display: grid;
		gap: 0.25rem;
		align-content: start;
	}
	.inline {
		display: flex;
		gap: var(--space-2);
	}
	label {
		font-weight: 600;
		font-size: var(--text-sm);
	}
	.hint {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-xs);
		font-weight: 400;
	}
	input,
	select,
	textarea {
		padding: 0.35rem 0.5rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-sm);
	}
	input,
	select {
		height: 2.125rem;
	}
	textarea {
		width: 100%;
		font-family: var(--font-id);
		resize: vertical;
	}
	[aria-invalid='true'] {
		border-color: var(--status-danger);
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
	.buttons {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-3);
	}
	button {
		height: 2.125rem;
		padding: 0 0.9rem;
		border: 1px solid var(--signal);
		border-radius: var(--radius-sm);
		background: var(--signal);
		color: var(--surface);
		font-weight: 700;
		cursor: pointer;
	}
	button.secondary {
		background: var(--surface);
		color: var(--signal);
	}
	button[aria-disabled='true'] {
		opacity: 0.6;
		cursor: progress;
	}
</style>

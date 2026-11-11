<script lang="ts">
	import type { FieldErrors } from '$lib/api/actions';
	import Icon from '$lib/components/Icon.svelte';
	import {
		BEFORE_PREFIX,
		DISPLAY_NAME_MAX,
		EMPTY_VALUES,
		FIELD_NAMES,
		hasChanges,
		problems,
		type FieldValues
	} from './tenant-form';

	/**
	 * Tenant create / edit inputs inside `ConfirmDialog` (HUB-270). Checks the values against the
	 * contract as the operator types (`tenant-form.ts`); a field shows its problem once it was left,
	 * and API pointers (`errors`, from the action) win.
	 */
	let {
		mode,
		before = EMPTY_VALUES,
		errors = {},
		values = $bindable()
	}: {
		mode: 'create' | 'edit' | 'setup';
		/** The values the dialog opened with (the tenant as loaded, for edit). */
		before?: FieldValues;
		errors?: FieldErrors;
		/** Owned by the page, which derives the dialog's `ready` with `canSend`. */
		values: FieldValues;
	} = $props();

	const id = $props.id();
	let touched = $state<Partial<Record<keyof FieldValues, boolean>>>({});

	const found = $derived(problems(values, mode));
	const changed = $derived(mode !== 'edit' || hasChanges(values, before));

	function errorFor(key: keyof FieldValues): string | null {
		const api = key === 'rps' || key === 'burst' ? null : errors[key];
		return api ?? (touched[key] ? (found[key] ?? null) : null);
	}
	const describe = (key: keyof FieldValues, hint = false) =>
		[hint ? `${id}-${key}-hint` : '', errorFor(key) ? `${id}-${key}-error` : '']
			.filter(Boolean)
			.join(' ') || undefined;
</script>

{#snippet problem(key: keyof FieldValues)}
	{@const e = errorFor(key)}
	{#if e}
		<p id="{id}-{key}-error" class="field-error"><Icon name="alert" size={14} />{e}</p>
	{/if}
{/snippet}

{#if mode !== 'create'}
	{#each FIELD_NAMES as key (key)}
		<input type="hidden" name="{BEFORE_PREFIX}{key}" value={before[key]} />
	{/each}
{/if}
<div class="grid">
	{#if mode === 'create'}
		<div class="field">
			<label for="{id}-tenant_id">Tenant id <span class="req">(required)</span></label>
			<input
				id="{id}-tenant_id"
				name="tenant_id"
				bind:value={values.tenant_id}
				onblur={() => (touched.tenant_id = true)}
				maxlength="64"
				autocomplete="off"
				spellcheck="false"
				placeholder="orders-service"
				aria-invalid={errorFor('tenant_id') ? 'true' : undefined}
				aria-describedby={describe('tenant_id', true)}
			/>
			<p id="{id}-tenant_id-hint" class="hint">
				Permanent. 3 to 64 letters, digits, "_" or "-". Callers never see it.
			</p>
			{@render problem('tenant_id')}
		</div>
	{/if}
	<div class="field">
		<label for="{id}-display_name">Display name <span class="req">(required)</span></label>
		<input
			id="{id}-display_name"
			name="display_name"
			bind:value={values.display_name}
			onblur={() => (touched.display_name = true)}
			maxlength={DISPLAY_NAME_MAX}
			autocomplete="off"
			aria-invalid={errorFor('display_name') ? 'true' : undefined}
			aria-describedby={describe('display_name')}
		/>
		{@render problem('display_name')}
	</div>
	<div class="field wide">
		<label for="{id}-description">Description</label>
		<input
			id="{id}-description"
			name="description"
			bind:value={values.description}
			autocomplete="off"
			aria-invalid={errors.description ? 'true' : undefined}
			aria-describedby={describe('description')}
		/>
		{@render problem('description')}
	</div>
	<div class="field wide">
		<label for="{id}-contact">Contact</label>
		<input
			id="{id}-contact"
			name="contact"
			bind:value={values.contact}
			autocomplete="off"
			placeholder="orders-team@example.com"
			aria-invalid={errors.contact ? 'true' : undefined}
			aria-describedby={describe('contact')}
		/>
		{@render problem('contact')}
	</div>
	<fieldset
		class="wide"
		aria-describedby="{id}-rl-hint{errors.rate_limit ? ` ${id}-rl-error` : ''}"
	>
		<legend>Rate limit</legend>
		<div class="pair">
			<div class="field">
				<label for="{id}-rps">Requests per second</label>
				<input
					id="{id}-rps"
					name="rps"
					inputmode="decimal"
					bind:value={values.rps}
					onblur={() => (touched.rps = true)}
					autocomplete="off"
					aria-invalid={errorFor('rps') || errors.rate_limit ? 'true' : undefined}
					aria-describedby={describe('rps')}
				/>
				{@render problem('rps')}
			</div>
			<div class="field">
				<label for="{id}-burst">Burst</label>
				<input
					id="{id}-burst"
					name="burst"
					inputmode="numeric"
					bind:value={values.burst}
					onblur={() => (touched.burst = true)}
					autocomplete="off"
					aria-invalid={errorFor('burst') || errors.rate_limit ? 'true' : undefined}
					aria-describedby={describe('burst')}
				/>
				{@render problem('burst')}
			</div>
		</div>
		<p id="{id}-rl-hint" class="hint">
			{mode === 'create'
				? 'Optional. Leave empty for the Hub default.'
				: 'Leave a field empty to keep its current value; a limit cannot be removed once set.'}
		</p>
		{#if errors.rate_limit}
			<p id="{id}-rl-error" class="field-error">
				<Icon name="alert" size={14} />{errors.rate_limit}
			</p>
		{/if}
	</fieldset>
</div>
{#if mode === 'edit' && !changed && Object.keys(found).length === 0}
	<p class="hint" role="status">Change at least one field to save.</p>
{/if}

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
		gap: var(--space-3);
	}
	.wide {
		grid-column: 1 / -1;
	}
	.field {
		display: grid;
		gap: 0.25rem;
		align-content: start;
	}
	label,
	legend {
		font-weight: 600;
		font-size: var(--text-sm);
	}
	fieldset {
		margin: 0;
		padding: var(--space-2) var(--space-3) var(--space-3);
		border: 1px solid var(--rule);
		border-radius: var(--radius-sm);
	}
	.pair {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
		gap: var(--space-3);
	}
	.req {
		color: var(--ink-muted);
		font-weight: 400;
	}
	input {
		width: 100%;
		padding: 0.4rem 0.55rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-sm);
	}
	[aria-invalid='true'] {
		border-color: var(--status-danger);
	}
	.hint {
		margin: var(--space-1) 0 0;
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
</style>

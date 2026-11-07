<script lang="ts">
	import FieldError from '$lib/components/FieldError.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import { CRED_NAMES, credentialProblem, type CredentialRow } from './account-form';

	/**
	 * Write-only credential inputs (HUB-291): a key (e.g. `passcode`, read by transformers as
	 * `creds.passcode`) and its secret value in a password field. The values are owned by the
	 * parent, which clears them right after submit. Nothing here ever displays a stored secret:
	 * the API never returns one.
	 */
	let {
		rows = $bindable(),
		error = null,
		showProblems = false,
		legend = 'Credentials',
		disabled = false
	}: {
		rows: CredentialRow[];
		/** From the action (API pointer `/credentials...`). */
		error?: string | null;
		showProblems?: boolean;
		legend?: string;
		disabled?: boolean;
	} = $props();

	const id = $props.id();
	let touched = $state(false);
	const problem = $derived(error ?? (touched || showProblems ? credentialProblem(rows) : null));

	function add() {
		rows.push({ key: '', value: '' });
	}
	function remove(i: number) {
		rows.splice(i, 1);
		if (!rows.length) rows.push({ key: '', value: '' });
	}
</script>

<fieldset class="fk-group" {disabled} aria-describedby="{id}-hint{problem ? ` ${id}-error` : ''}">
	<legend>{legend} <span class="fk-req">(write-only)</span></legend>
	<p id="{id}-hint" class="fk-hint">
		Transformers read each value as <code>creds.&lt;key&gt;</code>. Values are sent once, stored
		encrypted and never shown again, not even to admins; the form forgets them as soon as you
		submit.
	</p>
	<ul class="rows">
		{#each rows as r, i (i)}
			<li class="row">
				<div class="fk-field">
					<label for="{id}-k{i}">Key</label>
					<input
						id="{id}-k{i}"
						name={CRED_NAMES.key}
						bind:value={r.key}
						onblur={() => (touched = true)}
						maxlength="64"
						autocomplete="off"
						spellcheck="false"
						placeholder="passcode"
						aria-invalid={problem ? 'true' : undefined}
					/>
				</div>
				<div class="fk-field">
					<label for="{id}-v{i}">Secret value</label>
					<input
						id="{id}-v{i}"
						name={CRED_NAMES.value}
						type="password"
						bind:value={r.value}
						onblur={() => (touched = true)}
						autocomplete="new-password"
						spellcheck="false"
						aria-invalid={problem ? 'true' : undefined}
					/>
				</div>
				<button
					type="button"
					class="btn btn-quiet remove"
					onclick={() => remove(i)}
					aria-label="Remove credential {r.key || i + 1}"
				>
					<Icon name="close" size={14} />Remove
				</button>
			</li>
		{/each}
	</ul>
	<FieldError id="{id}-error" message={problem} />
	<div><button type="button" class="btn btn-quiet" onclick={add}>Add credential</button></div>
</fieldset>

<style>
	.rows {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}
	.row {
		display: grid;
		grid-template-columns: minmax(8rem, 14rem) minmax(0, 1fr) auto;
		gap: var(--space-2);
		align-items: end;
	}
	@media (max-width: 640px) {
		.row {
			grid-template-columns: minmax(0, 1fr) auto;
		}
		.row > :nth-child(2) {
			grid-column: 1 / -1;
			grid-row: 2;
		}
	}
	.remove {
		margin-bottom: 0.2rem;
	}
</style>

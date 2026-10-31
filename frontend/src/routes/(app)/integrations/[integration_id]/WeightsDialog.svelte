<script lang="ts">
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { Integration } from '$lib/api/types';
	import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import { weightsProblem } from './weights';

	/**
	 * HUB-260: new target weights for the active version's targets (gradual cutover). Weights must
	 * total 100; confirm stays disabled until they do and at least one changed. Publishing the new
	 * version right away is the default (contract `publish: true`); unticked, it stays a draft.
	 */
	let {
		open = $bindable(false),
		integration,
		onsuccess
	}: {
		open?: boolean;
		integration: Integration;
		onsuccess: (d: ActionSuccessData) => void;
	} = $props();

	const id = $props.id();
	const targets = $derived(integration.active?.spec.targets ?? []);
	const active = $derived(integration.active_version!);
	const next = $derived((integration.latest_version ?? active) + 1);

	let values = $state<(number | null)[]>([]);
	let publish = $state(true);
	// Reset to the current weights each time the dialog opens.
	$effect.pre(() => {
		if (open) {
			values = targets.map((t) => t.weight);
			publish = true;
		}
	});

	const numbers = $derived(
		values.map((v) => (typeof v === 'number' && Number.isInteger(v) ? v : NaN))
	);
	const total = $derived(numbers.reduce((a, b) => a + (Number.isNaN(b) ? 0 : b), 0));
	const problem = $derived(
		weightsProblem(
			numbers,
			targets.map((t) => t.weight)
		)
	);
</script>

<ConfirmDialog
	bind:open
	title="Set weights of {integration.integration_id}"
	verb={publish ? `Publish v${next}` : `Create draft v${next}`}
	busyVerb="Saving weights..."
	action="?/weights"
	tone="primary"
	subject={integration.integration_id}
	ready={!problem}
	{onsuccess}
>
	<p>
		Creates v{next} as a copy of the active v{active} with the weights below{publish
			? ` and publishes it. Traffic shifts within about 10 s.`
			: `, as a draft. v${active} stays active until you publish it.`}
	</p>
	{#snippet fields(errors)}
		<input type="hidden" name="expected_active_version" value={active} />
		<input type="hidden" name="targets" value={targets.length} />
		<fieldset class="weights" aria-describedby="{id}-total">
			<legend>Weights (%)</legend>
			{#each targets as t, n (n)}
				<input type="hidden" name="vendor_{n}" value={t.vendor_id} />
				<input type="hidden" name="account_{n}" value={t.account_id} />
				<div class="row">
					<label for="{id}-w{n}"><code>{t.vendor_id}/{t.account_id}</code></label>
					<span class="was">now {t.weight}%</span>
					<input
						id="{id}-w{n}"
						name="weight_{n}"
						type="number"
						inputmode="numeric"
						min="0"
						max="100"
						step="1"
						required
						bind:value={values[n]}
					/>
				</div>
			{/each}
			<p id="{id}-total" class="total" class:bad={total !== 100} role="status">
				{#if total === 100}
					<Icon name="check" size={14} />Total 100%
				{:else}
					<Icon name="alert" size={14} />Total {total}%: must be 100
				{/if}
			</p>
		</fieldset>
		{#if problem && total === 100}<p class="note">{problem}</p>{/if}
		{#if errors.weights}
			<p class="field-error"><Icon name="alert" size={14} />{errors.weights}</p>
		{/if}
		<label class="check">
			<input type="checkbox" name="publish" bind:checked={publish} />
			Publish v{next} now
		</label>
	{/snippet}
</ConfirmDialog>

<style>
	.weights {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding: var(--space-3);
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
	}
	legend {
		padding: 0 var(--space-1);
		font-weight: 600;
		font-size: var(--text-sm);
	}
	.row {
		display: grid;
		grid-template-columns: 1fr auto 6rem;
		align-items: center;
		gap: var(--space-3);
		font-size: var(--text-sm);
	}
	.row input {
		height: 2rem;
		padding: 0 0.5rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		text-align: right;
		font-variant-numeric: tabular-nums;
	}
	.was {
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.total {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		margin: 0;
		justify-content: flex-end;
		font-weight: 700;
		color: var(--status-ok);
	}
	.total.bad {
		color: var(--status-danger);
	}
	.note {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-sm);
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
	.check {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		font-size: var(--text-sm);
		font-weight: 600;
	}
</style>

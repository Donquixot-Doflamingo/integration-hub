<script lang="ts">
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { DlqBulkResult } from '$lib/api/types';
	import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import { bulkCopy, bulkWarnings, willAffect, type BulkKind } from './bulk';
	import { chipValue, FILTER_KEYS, filterFields, filterLabels, type Filters } from './filters';

	/**
	 * HUB-267: confirm a replay or purge of the dead letters the current filters match. The count
	 * comes from the API's dry run (`count`) and is shown before anything is submitted; the filters
	 * travel as hidden fields with that count (`expected`). Purge also needs the count typed, and the
	 * server refuses it when a fresh dry run no longer matches that count.
	 */
	let {
		open = $bindable(false),
		kind,
		count,
		filters,
		onsuccess
	}: {
		open?: boolean;
		kind: BulkKind;
		/** The dry run's answer. */
		count: DlqBulkResult;
		filters: Filters;
		onsuccess: (d: ActionSuccessData) => void;
	} = $props();

	const copy = $derived(bulkCopy(kind, count, filters));
	const warnings = $derived(bulkWarnings(kind, count, filters));
	const n = $derived(willAffect(count));
	const set = $derived(FILTER_KEYS.filter((k) => !!filters[k]));
</script>

<ConfirmDialog
	bind:open
	title={copy.title}
	verb={copy.verb}
	busyVerb={copy.busy}
	action={copy.action}
	tone={kind === 'purge' ? 'danger' : 'primary'}
	subject="the dead letters matching these filters"
	confirmText={kind === 'purge' ? String(n) : undefined}
	{onsuccess}
>
	<p>{copy.consequence}</p>
	{#if set.length}
		<ul class="filters" aria-label="Filters this {kind} uses">
			{#each set as key (key)}
				<li><span class="key">{filterLabels[key]}</span> {chipValue(key, filters)}</li>
			{/each}
		</ul>
	{/if}
	{#each warnings as w (w)}
		<p class="warn"><Icon name="alert" size={14} /><span>{w}</span></p>
	{/each}
	{#snippet fields()}
		{#each filterFields(filters) as [name, value] (name)}
			<input type="hidden" {name} {value} />
		{/each}
		<input type="hidden" name="expected" value={String(n)} />
	{/snippet}
</ConfirmDialog>

<style>
	.filters {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1) var(--space-3);
		margin: 0 0 var(--space-2);
		padding: 0;
		list-style: none;
	}
	.key {
		color: var(--ink-muted);
		font-weight: 600;
	}
	.warn {
		display: flex;
		align-items: flex-start;
		gap: 0.3rem;
		color: var(--status-warn);
		font-weight: 600;
	}
	.warn :global(.icon) {
		flex: none;
		margin-top: 0.15rem;
	}
</style>

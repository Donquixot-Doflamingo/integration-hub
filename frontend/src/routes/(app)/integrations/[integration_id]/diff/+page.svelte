<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import DiffPaths from '$lib/components/diff/DiffPaths.svelte';
	import DiffSplit from '$lib/components/diff/DiffSplit.svelte';
	import { summarize } from '$lib/utils/diff';
	import type { PageData } from './$types';
	import { compareSearch, versionOption, type DiffView } from './compare';

	let { data }: { data: PageData } = $props();

	const id = $derived(data.integrationId);
	const base = $derived(
		resolve('/(app)/integrations/[integration_id]/diff', { integration_id: id })
	);
	const viewHref = (view: DiffView) =>
		data.from !== null && data.to !== null ? base + compareSearch(data.from, data.to, view) : base;
	const views: { id: DiffView; label: string }[] = [
		{ id: 'split', label: 'Side by side' },
		{ id: 'paths', label: 'Changed paths' }
	];
</script>

<PageHeader
	title="Compare versions of {id}"
	summary="What changes between two versions of the spec. Old on the left, new on the right."
/>

<AsyncView result={data.versions} noun="versions of {id}" onretry={invalidateAll}>
	{#snippet children(list)}
		{#if list.items.length < 2}
			<EmptyState title="Nothing to compare yet">
				<p>{id} has {list.items.length === 1 ? 'one version' : 'no versions'}.</p>
			</EmptyState>
		{:else}
			<form class="pick" method="GET" action={base} aria-label="Choose versions">
				<div class="field">
					<label for="cmp-from">From (old)</label>
					<select id="cmp-from" name="from" value={String(data.from ?? '')}>
						{#each list.items as v (v.version)}<option value={String(v.version)}
								>{versionOption(v)}</option
							>{/each}
					</select>
				</div>
				<div class="field">
					<label for="cmp-to">To (new)</label>
					<select id="cmp-to" name="to" value={String(data.to ?? '')}>
						{#each list.items as v (v.version)}<option value={String(v.version)}
								>{versionOption(v)}</option
							>{/each}
					</select>
				</div>
				{#if data.view === 'paths'}<input type="hidden" name="view" value="paths" />{/if}
				<button type="submit">Compare</button>
				{#if data.from !== null && data.to !== null && data.from !== data.to}
					<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- base comes from resolve() -->
					<a class="swap" href={base + compareSearch(data.to, data.from, data.view)}>Swap sides</a>
				{/if}
			</form>
		{/if}
	{/snippet}
</AsyncView>

{#if data.diff}
	<AsyncView result={data.diff} noun="the diff v{data.from} to v{data.to}" onretry={invalidateAll}>
		{#snippet children(d)}
			<section class="result" aria-labelledby="diff-heading">
				<h2 id="diff-heading">v{d.from} to v{d.to}</h2>
				<p class="summary" role="status">{summarize(d.changes)}</p>
				<nav class="views" aria-label="Diff view">
					{#each views as v (v.id)}
						<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- viewHref builds on resolve() -->
						<a href={viewHref(v.id)} aria-current={data.view === v.id ? 'page' : undefined}
							>{v.label}</a
						>
					{/each}
				</nav>
				{#if data.view === 'paths'}
					<DiffPaths changes={d.changes} />
				{:else}
					<DiffSplit unified={d.unified} oldLabel="v{d.from}" newLabel="v{d.to}" />
				{/if}
			</section>
		{/snippet}
	</AsyncView>
{:else if data.from !== null && data.from === data.to}
	<div class="result">
		<EmptyState title="Choose two different versions">
			<p>Both sides are v{data.from}.</p>
		</EmptyState>
	</div>
{/if}

<p class="back">
	<a href={resolve('/(app)/integrations/[integration_id]', { integration_id: id })}>Back to {id}</a>
</p>

<style>
	.pick {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: var(--space-3);
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
		max-width: 46rem;
	}
	.field {
		display: grid;
		gap: 0.2rem;
	}
	label {
		font-size: var(--text-xs);
		font-weight: 600;
		color: var(--ink-muted);
	}
	select {
		height: 2rem;
		min-width: 9rem;
		padding: 0 0.5rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-sm);
	}
	button {
		height: 2rem;
		padding: 0 1rem;
		border: 1px solid var(--signal);
		border-radius: var(--radius-sm);
		background: var(--signal);
		color: var(--surface);
		font-weight: 700;
		cursor: pointer;
	}
	.swap {
		align-self: center;
		font-size: var(--text-sm);
		font-weight: 600;
	}
	.result {
		margin-top: var(--space-5);
	}
	.result h2 {
		font-size: var(--text-lg);
	}
	.summary {
		/* The summary lists the changed paths; long dotted paths must wrap on phones. */
		overflow-wrap: anywhere;
		margin: var(--space-1) 0 var(--space-3);
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.views {
		display: inline-flex;
		margin-bottom: var(--space-3);
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		overflow: hidden;
	}
	.views a {
		padding: 0.3rem 0.85rem;
		font-size: var(--text-sm);
		font-weight: 600;
		text-decoration: none;
		color: var(--ink);
		background: var(--surface);
	}
	.views a + a {
		border-left: 1px solid var(--rule-strong);
	}
	.views a[aria-current='page'] {
		background: var(--signal);
		color: var(--surface);
	}
	.back {
		margin-top: var(--space-5);
		font-weight: 600;
	}
</style>

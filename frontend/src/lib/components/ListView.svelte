<script lang="ts" module>
	export interface Column {
		label: string;
		/** Right-align numbers (admin-console-ux §5). */
		numeric?: boolean;
	}
</script>

<script lang="ts" generics="T">
	import type { Snippet } from 'svelte';
	import type { ApiResult } from '$lib/api/client';
	import { HubApiError } from '$lib/api/errors';
	import type { ListPage } from '$lib/api/list';
	import EmptyState from './EmptyState.svelte';
	import ErrorPanel from './ErrorPanel.svelte';

	let {
		result,
		noun,
		caption,
		columns,
		row,
		empty,
		emptyTitle,
		onretry
	}: {
		/** The list call from `load` (`api.call(...)`), never rejected. */
		result: Promise<ApiResult<ListPage<T>>>;
		/** Plural noun for status text: "integrations". */
		noun: string;
		/** Accessible table caption. */
		caption: string;
		columns: Column[];
		/** Renders the `<td>` cells of one item (first cell is the row header). */
		row: Snippet<[T]>;
		emptyTitle: string;
		empty?: Snippet;
		onretry?: () => void;
	} = $props();

	const SKELETON_ROWS = 6;

	function asError(e: unknown): HubApiError {
		return e instanceof HubApiError ? e : HubApiError.network(e);
	}
</script>

{#snippet head()}
	<thead>
		<tr>
			{#each columns as c (c.label)}
				<th scope="col" class:numeric={c.numeric}>{c.label}</th>
			{/each}
		</tr>
	</thead>
{/snippet}

{#await result}
	<div class="table-scroll">
		<table aria-busy="true" aria-describedby="list-loading">
			<caption class="visually-hidden">{caption}</caption>
			{@render head()}
			<tbody>
				{#each { length: SKELETON_ROWS }, i (i)}
					<tr class="skeleton" aria-hidden="true">
						{#each columns as c (c.label)}<td><span class="bar"></span></td>{/each}
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
	<p id="list-loading" class="meta" role="status">Loading {noun}...</p>
{:then res}
	{#if !res.ok}
		<ErrorPanel error={res.error} context={`Could not load ${noun}`} {onretry} />
	{:else if res.data.items.length === 0}
		<EmptyState title={emptyTitle}>{@render empty?.()}</EmptyState>
	{:else}
		<!-- Wide tables scroll sideways inside this region, so it must be focusable for keyboard users
		     (WCAG 2.1.1, axe scrollable-region-focusable). -->
		<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
		<div class="table-scroll" role="region" aria-label={caption} tabindex="0">
			<table>
				<caption class="visually-hidden">{caption}</caption>
				{@render head()}
				<tbody>
					{#each res.data.items as item, i (i)}
						<tr>{@render row(item)}</tr>
					{/each}
				</tbody>
			</table>
		</div>
		<p class="meta" role="status">
			{res.data.items.length}
			{res.data.items.length === 1 ? 'row' : 'rows'} shown.
			{#if res.data.next_cursor}More are available; paging arrives with this screen's own ticket.{/if}
		</p>
	{/if}
{:catch e}
	<ErrorPanel error={asError(e)} context={`Could not load ${noun}`} {onretry} />
{/await}

<style>
	.table-scroll {
		overflow-x: auto;
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: var(--text-sm);
		font-variant-numeric: tabular-nums;
	}
	/* Cells come from the page's row snippet, hence :global inside this table. */
	table :global(th),
	table :global(td) {
		height: var(--row-height);
		padding: 0 var(--space-3);
		text-align: left;
		white-space: nowrap;
		border-bottom: 1px solid var(--rule);
		background: var(--surface);
	}
	thead th {
		background: var(--surface-sunken);
		color: var(--ink-muted);
		font-weight: 600;
		font-size: var(--text-xs);
	}
	tbody :global(tr:last-child > *) {
		border-bottom: 0;
	}
	tbody :global(th) {
		font-weight: 600;
	}
	tbody :global(tr:hover > *) {
		background: var(--signal-wash);
	}
	/* Sticky first column so the row's id stays visible while scrolling sideways. */
	table :global(tr > :first-child) {
		position: sticky;
		left: 0;
		z-index: 1;
	}
	.numeric,
	table :global(td.numeric) {
		text-align: right;
	}
	.bar {
		display: block;
		height: 0.6rem;
		width: 70%;
		border-radius: 2px;
		background: var(--surface-sunken);
		animation: pulse 1.4s ease-in-out infinite;
	}
	@keyframes pulse {
		50% {
			opacity: 0.45;
		}
	}
	.meta {
		margin: var(--space-2) 0 0;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
</style>

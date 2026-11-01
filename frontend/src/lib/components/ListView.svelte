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
	import type { ListPage } from '$lib/api/list';
	import AsyncView from './AsyncView.svelte';
	import EmptyState from './EmptyState.svelte';

	/**
	 * Shared list view (HUB-255): an `AsyncView` whose loading state is a table skeleton with the
	 * real columns, whose empty state is `EmptyState`, and whose data is an accessible table with a
	 * sticky first column. `footer` receives the page (pagination, HUB-256).
	 */
	let {
		result,
		noun,
		caption,
		columns,
		row,
		empty,
		emptyTitle,
		filtered = false,
		clearHref,
		footer,
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
		/** True when filters are active, so an empty page means "filters hide everything". */
		filtered?: boolean;
		clearHref?: string;
		/** Below the table, e.g. a `Pager`. Replaces the default row count. */
		footer?: Snippet<[ListPage<T>]>;
		onretry?: () => void;
	} = $props();

	const SKELETON_ROWS = 6;
	const loadingId = $props.id();
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

<AsyncView {result} {noun} {onretry}>
	{#snippet loading()}
		<div class="table-scroll">
			<table aria-busy="true" aria-describedby={loadingId}>
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
		<p id={loadingId} class="meta" role="status">Loading {noun}...</p>
	{/snippet}
	{#snippet children(page)}
		{#if page.items.length === 0}
			<EmptyState title={emptyTitle} {filtered} {clearHref}>{@render empty?.()}</EmptyState>
		{:else}
			<!-- Wide tables scroll sideways inside this region, so it must be focusable for keyboard users
			     (WCAG 2.1.1, axe scrollable-region-focusable). -->
			<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
			<div class="table-scroll" role="region" aria-label={caption} tabindex="0">
				<table>
					<caption class="visually-hidden">{caption}</caption>
					{@render head()}
					<tbody>
						{#each page.items as item, i (i)}
							<tr>{@render row(item)}</tr>
						{/each}
					</tbody>
				</table>
			</div>
			{#if footer}
				{@render footer(page)}
			{:else}
				<p class="meta" role="status">
					{page.items.length}
					{page.items.length === 1 ? 'row' : 'rows'} shown.
					{#if page.next_cursor}More are available; paging arrives with this screen's own ticket.{/if}
				</p>
			{/if}
		{/if}
	{/snippet}
</AsyncView>

<style>
	.table-scroll {
		/* Containing block for absolutely positioned cell content (CopyButton's live region), so it
		   scrolls with the table instead of widening the page (HUB-273). */
		position: relative;
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

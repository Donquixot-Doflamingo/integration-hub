<script lang="ts">
	import { nextPaging, pageNumber, previousPaging, type Paging } from '$lib/utils/paging';
	import Icon from './Icon.svelte';

	/**
	 * Cursor pagination (admin-console-ux §5): Previous / Next links, no "page 7 of 92" because the
	 * API returns no totals. `href` turns a paging state into a link that keeps the current filters.
	 */
	let {
		paging,
		nextCursor,
		count,
		noun,
		href
	}: {
		paging: Paging;
		nextCursor: string | null;
		/** Items on this page. */
		count: number;
		/** Plural noun: "integrations". */
		noun: string;
		href: (p: Paging) => string;
	} = $props();

	const prev = $derived(previousPaging(paging));
	const next = $derived(nextCursor ? nextPaging(paging, nextCursor) : null);
</script>

<nav class="pager" aria-label="Pagination">
	<p class="where" role="status">
		{pageNumber(paging) === null ? 'This page' : `Page ${pageNumber(paging)}`}: {count}
		{count === 1 ? noun.replace(/s$/, '') : noun}{next ? ', more after this page' : ''}.
	</p>
	<!-- A single page needs no Previous / Next (HUB-287: two inert buttons looked clickable). -->
	{#if prev || next}
		<ul>
			<li>
				{#if prev}
					<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- href() builds on resolve() -->
					<a class="back" href={href(prev)} rel="prev"><Icon name="chevron" size={14} />Previous</a>
				{:else}
					<span class="off back" aria-disabled="true"
						><Icon name="chevron" size={14} />Previous</span
					>
				{/if}
			</li>
			<li>
				{#if next}
					<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- href() builds on resolve() -->
					<a href={href(next)} rel="next">Next<Icon name="chevron" size={14} /></a>
				{:else}
					<span class="off" aria-disabled="true">Next<Icon name="chevron" size={14} /></span>
				{/if}
			</li>
		</ul>
	{/if}
</nav>

<style>
	.pager {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
		margin-top: var(--space-3);
		font-size: var(--text-sm);
	}
	.where {
		margin: 0;
		color: var(--ink-muted);
	}
	ul {
		display: flex;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}
	a,
	.off {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		padding: 0.3rem 0.75rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-weight: 600;
		text-decoration: none;
	}
	a:hover {
		border-color: var(--signal);
	}
	.off {
		color: var(--ink-muted);
		border-color: var(--rule);
		border-style: dashed;
		background: transparent;
		cursor: default;
		opacity: 0.7;
	}
	.back :global(.icon) {
		transform: rotate(180deg);
	}
</style>

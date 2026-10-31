<script lang="ts">
	import type { Snippet } from 'svelte';

	/**
	 * Empty data views (HUB-255). Say why it is empty and what to do. `filtered` means "filters hide
	 * everything" (offer `clearHref`), as opposed to "nothing exists yet" (admin-console-ux §6).
	 */
	let {
		title,
		filtered = false,
		clearHref,
		children
	}: {
		title: string;
		filtered?: boolean;
		/** Link that removes all filters (shown when `filtered`). */
		clearHref?: string;
		children?: Snippet;
	} = $props();
</script>

<div class="empty" class:filtered role="status">
	<p class="title">{title}</p>
	{#if children}<div class="detail">{@render children()}</div>{/if}
	{#if filtered && clearHref}
		<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- clearHref is a resolved path from the page -->
		<p class="action"><a href={clearHref}>Clear all filters</a></p>
	{/if}
</div>

<style>
	.empty {
		padding: var(--space-5) var(--space-4);
		border: 1px dashed var(--rule-strong);
		border-radius: var(--radius-md);
		background: var(--surface);
		max-width: 46rem;
	}
	.title {
		margin: 0;
		font-weight: 700;
	}
	.detail {
		margin-top: var(--space-1);
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.detail :global(p) {
		margin: 0;
	}
	.action {
		margin: var(--space-2) 0 0;
		font-size: var(--text-sm);
		font-weight: 600;
	}
</style>

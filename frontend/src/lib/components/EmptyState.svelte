<script lang="ts">
	import type { Snippet } from 'svelte';

	let { title, children }: { title: string; children?: Snippet } = $props();
</script>

<div class="empty" role="status">
	<p class="title">{title}</p>
	{#if children}<div class="detail">{@render children()}</div>{/if}
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
</style>

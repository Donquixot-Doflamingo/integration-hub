<script lang="ts">
	/**
	 * Loading placeholder for detail views (HUB-255): grey bars in the shape of the content, so the
	 * layout does not jump when data arrives. Tables use `TableSkeleton`. Announces `label` politely.
	 */
	let { label, lines = 4 }: { label: string; lines?: number } = $props();
</script>

<div class="skeleton" aria-busy="true">
	{#each { length: lines }, i (i)}
		<span class="bar" style:width="{[62, 88, 74, 45, 80, 56][i % 6]}%" aria-hidden="true"></span>
	{/each}
	<p class="label" role="status">{label}</p>
</div>

<style>
	.skeleton {
		display: grid;
		gap: var(--space-3);
		max-width: 46rem;
		padding: var(--space-4);
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
	}
	.bar {
		display: block;
		height: 0.7rem;
		border-radius: 2px;
		background: var(--surface-sunken);
		animation: pulse 1.4s ease-in-out infinite;
	}
	@keyframes pulse {
		50% {
			opacity: 0.45;
		}
	}
	.label {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
</style>

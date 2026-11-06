<script lang="ts">
	import { pageTitle } from '$lib/app-info';

	let { title, summary }: { title: string; summary?: string } = $props();
</script>

<svelte:head>
	<title>{pageTitle(title)}</title>
</svelte:head>

<header class="page-header">
	<h1>{title}</h1>
	{#if summary}<p>{summary}</p>{/if}
</header>

<style>
	.page-header {
		margin-bottom: var(--space-4);
		max-width: 60rem;
	}
	h1 {
		font-size: var(--text-xl);
		letter-spacing: -0.01em;
		/* Ids like req_01M6SFX4FP8PYDN0FB8FVX5C3A are one long word: break them on a phone. */
		overflow-wrap: anywhere;
	}
	p {
		margin: var(--space-1) 0 0;
		color: var(--ink-muted);
	}
</style>

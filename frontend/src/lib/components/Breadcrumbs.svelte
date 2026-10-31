<script lang="ts">
	import { resolve } from '$app/paths';
	import type { Pathname } from '$app/types';
	import { breadcrumbsFor } from '$lib/nav';
	import Icon from './Icon.svelte';

	let { pathname }: { pathname: string } = $props();

	const crumbs = $derived(breadcrumbsFor(pathname));
	// Deeper crumbs are sub-paths of the current URL, so they are real pathnames of this app.
	const href = (p: string) => resolve(p as Pathname);
</script>

<nav class="crumbs" aria-label="Breadcrumb">
	<ol>
		{#each crumbs as c, i (c.href)}
			<li>
				{#if i > 0}<Icon name="chevron" size={14} />{/if}
				{#if i === crumbs.length - 1}
					<a href={href(c.href)} aria-current="page">{c.label}</a>
				{:else}
					<a href={href(c.href)}>{c.label}</a>
				{/if}
			</li>
		{/each}
	</ol>
</nav>

<style>
	ol {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1);
		margin: 0;
		padding: 0;
		list-style: none;
		font-size: var(--text-sm);
	}
	li {
		display: flex;
		align-items: center;
		gap: var(--space-1);
		color: var(--ink-muted);
		min-width: 0;
	}
	a {
		color: var(--ink-muted);
		text-decoration: none;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	a:hover {
		color: var(--ink);
		text-decoration: underline;
	}
	a[aria-current='page'] {
		color: var(--ink);
		font-weight: 600;
	}
</style>

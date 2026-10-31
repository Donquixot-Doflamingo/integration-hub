<script lang="ts">
	import { resolve } from '$app/paths';
	import Icon from '$lib/components/Icon.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import { appName } from '$lib/app-info';
	import { sections } from '$lib/nav';
</script>

<PageHeader
	title={appName}
	summary="Control plane for the Integration Hub: integrations, health, investigation and recovery."
/>

<ul class="sections">
	{#each sections as s (s.href)}
		<li>
			<a href={resolve(s.href)}>
				<Icon name={s.icon} />
				<span class="label">{s.label}</span>
			</a>
			<p>{s.summary}</p>
		</li>
	{/each}
</ul>

<style>
	.sections {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(17rem, 1fr));
		gap: 0;
		margin: 0;
		padding: 0;
		list-style: none;
		border-top: 1px solid var(--rule);
		max-width: 72rem;
	}
	li {
		padding: var(--space-3) var(--space-4) var(--space-4) 0;
		border-bottom: 1px solid var(--rule);
	}
	a {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		font-weight: 700;
		font-size: var(--text-md);
		text-decoration: none;
	}
	a:hover .label {
		text-decoration: underline;
	}
	p {
		margin: var(--space-1) 0 0;
		color: var(--ink-muted);
		font-size: var(--text-sm);
		max-width: 34ch;
	}
</style>

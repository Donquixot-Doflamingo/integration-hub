<script lang="ts">
	import Icon from './Icon.svelte';

	/**
	 * Summary of what blocks a form (HUB-290+): the problems the console found and API details no
	 * field claimed, so nothing is ever silently swallowed. Announced as an alert.
	 */
	let {
		title = 'Fix these before saving',
		items
	}: {
		title?: string;
		items: { label?: string; message: string }[];
	} = $props();
</script>

{#if items.length}
	<div class="fk-summary" role="alert">
		<p><Icon name="alert" size={16} /> {title}</p>
		<ul>
			{#each items as item, i (i)}
				<li>
					{#if item.label}<code>{item.label}</code>:
					{/if}{item.message}
				</li>
			{/each}
		</ul>
	</div>
{/if}

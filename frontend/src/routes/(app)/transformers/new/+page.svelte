<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import type { PageData } from './$types';
	import TransformerAuthoring from '../TransformerAuthoring.svelte';
	import { emptyValues } from '../authoring';

	/** A new transformer, version 1 (HUB-293, `createTransformerVersion`, editor). */
	let { data }: { data: PageData } = $props();
</script>

<PageHeader
	title="New transformer"
	summary="A request transformer turns the caller's input into one vendor endpoint's request; a response transformer shapes what the caller gets back. Samples prove both."
/>

<RoleGate access={data.access} ops={['createTransformerVersion', 'runTransformerGoldens']}>
	<AsyncView result={data.lookups} noun="the vendor endpoints" onretry={invalidateAll}>
		{#snippet children(l)}
			<TransformerAuthoring mode="new" initial={emptyValues()} endpoints={l.endpoints} />
		{/snippet}
	</AsyncView>
	{#snippet denied(role)}
		<p class="fk-note" role="note">
			Read only. Creating transformers needs the {role} role.
			<a href={resolve('/transformers')}>Back to transformers</a>
		</p>
	{/snippet}
</RoleGate>

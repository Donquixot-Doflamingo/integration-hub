<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import type { PageData } from './$types';
	import TransformerAuthoring from '../../TransformerAuthoring.svelte';
	import { valuesFromVersion } from '../../authoring';

	/** The next version of a transformer, starting from the latest one (HUB-293, editor). */
	let { data }: { data: PageData } = $props();
	const id = $derived(data.transformerId);
</script>

<PageHeader
	title="New version of {id}"
	summary="Starts from the latest version: change the source or the fixtures, run the goldens, then create it."
/>

<RoleGate access={data.access} ops={['createTransformerVersion', 'runTransformerGoldens']}>
	<AsyncView result={data.start} noun="transformer {id}" onretry={invalidateAll}>
		{#snippet children(s)}
			{#key s.transformer.latest_version}
				<p class="fk-note">
					Based on {s.transformer.latest.ref} by {s.transformer.latest.author}: {s.transformer
						.latest.reason}
				</p>
				<TransformerAuthoring
					mode="version"
					initial={valuesFromVersion(s.transformer.latest)}
					latestVersion={s.transformer.latest_version}
					capabilities={s.capabilities}
					endpointRefs={s.endpointRefs}
				/>
			{/key}
		{/snippet}
	</AsyncView>
	{#snippet denied(role)}
		<p class="fk-note" role="note">
			Read only. Creating transformer versions needs the {role} role.
			<a href={resolve('/(app)/transformers/[transformer_id]', { transformer_id: id })}
				>Back to {id}</a
			>
		</p>
	{/snippet}
</RoleGate>

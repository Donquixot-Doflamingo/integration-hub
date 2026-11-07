<script lang="ts">
	import { onMount } from 'svelte';
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import ChangeNotice from '$lib/components/ChangeNotice.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import { takeFlash, type Flash } from '$lib/stores/flash.svelte';
	import VersionView from '$lib/components/transformer/VersionView.svelte';
	import type { PageData } from './$types';

	/** One immutable transformer version (HUB-268): source, fixtures, goldens. */
	let { data }: { data: PageData } = $props();

	const ref = $derived(`${data.transformerId}@${data.version}`);
	const scoped = $derived(data.access.tenantScoped && data.access.known);
	// "Created T1@8" from the authoring page (HUB-293).
	let notice = $state.raw<Flash | null>(null);
	onMount(() => {
		notice = takeFlash();
	});
</script>

<PageHeader title={ref} summary="One immutable version of the transformer." />

{#if notice}
	<ChangeNotice {...notice} ondismiss={() => (notice = null)} />
{/if}

<p class="back">
	<a href={resolve('/(app)/transformers/[transformer_id]', { transformer_id: data.transformerId })}
		>All versions of {data.transformerId}</a
	>
</p>

{#if scoped}
	<p class="note" role="note">
		Transformers map to vendor APIs, which tenant viewers cannot see. Ask the platform team about a
		mapping.
	</p>
{:else}
	<AsyncView result={data.detail} noun="transformer version {ref}" onretry={invalidateAll}>
		{#snippet children(v)}
			<VersionView version={v} access={data.access} />
		{/snippet}
	</AsyncView>
{/if}

<style>
	.back {
		margin: 0 0 var(--space-4);
		font-size: var(--text-sm);
	}
	.note {
		color: var(--ink-muted);
		max-width: 46rem;
	}
</style>

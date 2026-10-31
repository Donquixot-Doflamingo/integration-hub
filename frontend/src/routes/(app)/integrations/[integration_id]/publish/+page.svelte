<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import ChangeNotice from '$lib/components/ChangeNotice.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import DraftEditor from './DraftEditor.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const id = $derived(data.integrationId);
	const detailHref = $derived(
		resolve('/(app)/integrations/[integration_id]', { integration_id: id })
	);
	let notice = $state.raw<{
		message: string;
		changeId: string | null;
		requestId: string | null;
	} | null>(null);
</script>

<PageHeader
	title="Publish {id}"
	summary="Edit the draft spec, run the publish checks, compare it with the active version, then publish with a reason."
/>

{#if notice}
	<ChangeNotice {...notice} ondismiss={() => (notice = null)} />
{/if}

<RoleGate access={data.access}>
	<AsyncView result={data.base} noun="the draft of {id}" onretry={invalidateAll}>
		{#snippet children(b)}
			<DraftEditor
				integration={b.integration}
				draft={b.draft}
				diff={data.diff}
				onnotice={(message, changeId, requestId) => (notice = { message, changeId, requestId })}
			/>
		{/snippet}
	</AsyncView>
	{#snippet denied(role)}
		<p class="readonly" role="note">
			Read only. Editing and publishing versions needs the {role} role.
			<a href={detailHref}>Back to {id}</a>
		</p>
	{/snippet}
</RoleGate>

<style>
	.readonly {
		color: var(--ink-muted);
	}
</style>

<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { sectionFor } from '$lib/nav';
	import { formatNumber, formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/dlq')!;
</script>

<PageHeader title={section.label} summary={section.summary} />

<ListView
	result={data.list}
	noun="dead letters"
	caption="Dead-lettered requests"
	emptyTitle="The dead-letter queue is empty"
	columns={[
		{ label: 'Request id' },
		{ label: 'Tenant' },
		{ label: 'Integration' },
		{ label: 'Reason' },
		{ label: 'Attempts', numeric: true },
		{ label: 'Dead-lettered' },
		{ label: 'Replay' },
		{ label: 'Expires' }
	]}
	onretry={invalidateAll}
>
	{#snippet row(d)}
		<th scope="row"><code class="id">{d.request_id}</code></th>
		<td>{d.tenant_id}</td>
		<td><code class="id">{d.integration_id}</code></td>
		<td title={d.message ?? undefined}><code>{d.reason}</code></td>
		<td class="numeric">{formatNumber(d.attempts)}</td>
		<td>{formatUtc(d.dead_lettered_at)}</td>
		<td>
			{#if d.replayed_at}
				<StatusBadge tone="ok" icon="retry" label={`Replayed ${formatUtc(d.replayed_at)}`} />
			{:else}
				<StatusBadge tone="warn" icon="dlq" label="Waiting" />
			{/if}
		</td>
		<td>{formatUtc(d.expires_at)}</td>
	{/snippet}
	{#snippet empty()}
		<p>Nothing is waiting for a replay. Requests land here after their final failed attempt.</p>
	{/snippet}
</ListView>

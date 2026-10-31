<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { sectionFor } from '$lib/nav';
	import { formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/integrations')!;
</script>

<PageHeader title={section.label} summary={section.summary} />

<ListView
	result={data.list}
	noun="integrations"
	caption="Integrations"
	emptyTitle="No integrations yet"
	columns={[
		{ label: 'Integration' },
		{ label: 'Owner tenant' },
		{ label: 'Capability' },
		{ label: 'Active version', numeric: true },
		{ label: 'State' },
		{ label: 'Targets' },
		{ label: 'Updated' }
	]}
	onretry={invalidateAll}
>
	{#snippet row(i)}
		<th scope="row"><code class="id">{i.integration_id}</code></th>
		<td>{i.owner_tenant}</td>
		<td><code>{i.capability}</code></td>
		<td class="numeric">{i.active_version == null ? 'None' : `v${i.active_version}`}</td>
		<td>
			{#if i.paused}
				<StatusBadge tone="neutral" icon="pause" label="Paused" />
			{:else}
				<StatusBadge tone="ok" icon="closed" label="Live" />
			{/if}
		</td>
		<td>
			{(i.targets ?? []).map((t) => `${t.vendor_id}/${t.account_id} ${t.weight}%`).join(', ')}
		</td>
		<td>{formatUtc(i.updated_at)}</td>
	{/snippet}
	{#snippet empty()}
		<p>Integrations appear here once a team creates one with hubctl or the admin API.</p>
	{/snippet}
</ListView>

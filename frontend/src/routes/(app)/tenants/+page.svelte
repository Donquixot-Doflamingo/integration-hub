<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { sectionFor } from '$lib/nav';
	import { formatNumber, formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/tenants')!;
</script>

<PageHeader title={section.label} summary={section.summary} />

<ListView
	result={data.list}
	noun="tenants"
	caption="Tenants"
	emptyTitle="No tenants yet"
	columns={[
		{ label: 'Tenant' },
		{ label: 'Name' },
		{ label: 'Status' },
		{ label: 'Integrations owned', numeric: true },
		{ label: 'Active API keys', numeric: true },
		{ label: 'Updated' }
	]}
	onretry={invalidateAll}
>
	{#snippet row(t)}
		<th scope="row"><code class="id">{t.tenant_id}</code></th>
		<td>{t.display_name}</td>
		<td>
			{#if t.disabled}
				<StatusBadge tone="neutral" icon="pause" label="Disabled" />
			{:else}
				<StatusBadge tone="ok" icon="closed" label="Active" />
			{/if}
		</td>
		<td class="numeric">{formatNumber(t.integrations_owned)}</td>
		<td class="numeric">{formatNumber(t.active_api_keys)}</td>
		<td>{formatUtc(t.updated_at)}</td>
	{/snippet}
	{#snippet empty()}
		<p>Create a tenant for each calling team before issuing API keys.</p>
	{/snippet}
</ListView>

<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import { sectionFor } from '$lib/nav';
	import { formatNumber, formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/transformers')!;
</script>

<PageHeader title={section.label} summary={section.summary} />

<ListView
	result={data.list}
	noun="transformers"
	caption="Transformers"
	emptyTitle="No transformers yet"
	columns={[
		{ label: 'Transformer' },
		{ label: 'Kind' },
		{ label: 'Capability' },
		{ label: 'Vendor endpoint' },
		{ label: 'Latest version', numeric: true },
		{ label: 'Used by active versions', numeric: true },
		{ label: 'Updated' }
	]}
	onretry={invalidateAll}
>
	{#snippet row(t)}
		<th scope="row"><code class="id">{t.transformer_id}</code></th>
		<td>{t.kind}</td>
		<td><code>{t.capability}</code></td>
		<td><code>{t.vendor_endpoint}</code></td>
		<td class="numeric">{t.latest_version == null ? 'None' : `v${t.latest_version}`}</td>
		<td class="numeric">{formatNumber(t.used_by_active_versions)}</td>
		<td>{formatUtc(t.updated_at)}</td>
	{/snippet}
	{#snippet empty()}
		<p>Transformers are added by the platform team through pull requests with golden fixtures.</p>
	{/snippet}
</ListView>

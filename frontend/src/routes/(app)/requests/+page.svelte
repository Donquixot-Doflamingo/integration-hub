<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { sectionFor } from '$lib/nav';
	import { requestStatus } from '$lib/status';
	import { formatDurationMs, formatNumber, formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/requests')!;
</script>

<PageHeader title={section.label} summary={section.summary} />

<ListView
	result={data.list}
	noun="requests"
	caption="Recent requests"
	emptyTitle="No requests in this time range"
	columns={[
		{ label: 'Request id' },
		{ label: 'Tenant' },
		{ label: 'Integration' },
		{ label: 'Status' },
		{ label: 'Attempts', numeric: true },
		{ label: 'Duration', numeric: true },
		{ label: 'Created' }
	]}
	onretry={invalidateAll}
>
	{#snippet row(r)}
		{@const s = requestStatus[r.status]}
		<th scope="row"><code class="id">{r.request_id}</code></th>
		<td>{r.tenant_id}</td>
		<td><code class="id">{r.integration_id}</code></td>
		<td>
			<StatusBadge tone={s.tone} icon={s.icon} label={r.code ? `${s.label}: ${r.code}` : s.label} />
		</td>
		<td class="numeric">{formatNumber(r.attempts)}</td>
		<td class="numeric">{formatDurationMs(r.duration_ms)}</td>
		<td>{formatUtc(r.created_at)}</td>
	{/snippet}
	{#snippet empty()}
		<p>Requests show up here as soon as callers invoke an integration.</p>
	{/snippet}
</ListView>

<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import { sectionFor } from '$lib/nav';
	import { formatBytes, formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/snapshots')!;
</script>

<PageHeader title={section.label} summary={section.summary} />

<ListView
	result={data.list}
	noun="snapshots"
	caption="Registry snapshots, newest first"
	emptyTitle="No snapshots published yet"
	columns={[
		{ label: 'Version', numeric: true },
		{ label: 'Summary' },
		{ label: 'Author' },
		{ label: 'Change' },
		{ label: 'Size', numeric: true },
		{ label: 'Created' }
	]}
	onretry={invalidateAll}
>
	{#snippet row(s)}
		<th scope="row" class="numeric">{s.version}</th>
		<td>{s.summary}</td>
		<td>{s.author}</td>
		<td><code class="id">{s.change_id}</code></td>
		<td class="numeric">{formatBytes(s.size_bytes)}</td>
		<td>{formatUtc(s.created_at)}</td>
	{/snippet}
	{#snippet empty()}
		<p>The first publish of any registry object creates snapshot 1.</p>
	{/snippet}
</ListView>

<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import { sectionFor } from '$lib/nav';
	import { formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/change-log')!;
</script>

<PageHeader title={section.label} summary={section.summary} />

<ListView
	result={data.list}
	noun="change log entries"
	caption="Change log, newest first"
	emptyTitle="No changes recorded yet"
	columns={[
		{ label: 'Change' },
		{ label: 'When' },
		{ label: 'Actor' },
		{ label: 'Action' },
		{ label: 'Object' },
		{ label: 'Reason' }
	]}
	onretry={invalidateAll}
>
	{#snippet row(c)}
		<th scope="row"><code class="id">{c.change_id}</code></th>
		<td>{formatUtc(c.at)}</td>
		<td>{c.actor}</td>
		<td><code>{c.action}</code></td>
		<td>{c.object_type} <code class="id">{c.object_id}</code></td>
		<td class="reason">{c.reason}</td>
	{/snippet}
	{#snippet empty()}
		<p>Every mutation made through the admin API is recorded here with its reason.</p>
	{/snippet}
</ListView>

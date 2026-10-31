<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { sectionFor } from '$lib/nav';
	import { breakerSeverity, breakerStatus } from '$lib/status';
	import { formatDurationMs, formatNumber, formatRatio, formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/health')!;

	// Worst first (admin-console-ux §4): open, half-open, closed; then most parked.
	const sorted = $derived(
		data.list.then((r) =>
			r.ok
				? {
						...r,
						data: {
							...r.data,
							items: [...r.data.items].sort(
								(a, b) =>
									breakerSeverity[a.state] - breakerSeverity[b.state] ||
									(b.parked_count ?? 0) - (a.parked_count ?? 0)
							)
						}
					}
				: r
		)
	);
</script>

<PageHeader title={section.label} summary={section.summary} />

<ListView
	result={sorted}
	noun="breakers"
	caption="Circuit breakers, worst first"
	emptyTitle="No breakers reported"
	columns={[
		{ label: 'Account endpoint' },
		{ label: 'Vendor' },
		{ label: 'State' },
		{ label: 'Failure rate', numeric: true },
		{ label: 'Parked', numeric: true },
		{ label: 'Oldest parked', numeric: true },
		{ label: 'Updated' }
	]}
	onretry={invalidateAll}
>
	{#snippet row(b)}
		{@const s = breakerStatus[b.state]}
		<th scope="row"><code class="id">{b.account_id}</code> <code>{b.endpoint_ref}</code></th>
		<td>{b.vendor_id}</td>
		<td>
			<StatusBadge
				tone={s.tone}
				icon={s.icon}
				label={b.forced && b.forced !== 'none' ? `${s.label} (forced)` : s.label}
			/>
		</td>
		<td class="numeric">{formatRatio(b.failure_ratio)}</td>
		<td class="numeric">{formatNumber(b.parked_count)}</td>
		<td class="numeric">{formatDurationMs(b.parked_oldest_age_ms)}</td>
		<td>{formatUtc(b.updated_at)}</td>
	{/snippet}
	{#snippet empty()}
		<p>Breakers appear once accounts have handled traffic.</p>
	{/snippet}
</ListView>

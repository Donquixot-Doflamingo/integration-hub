<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { Snapshot } from '$lib/api/types';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Pager from '$lib/components/Pager.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { sectionFor } from '$lib/nav';
	import { formatBytes, formatUtc } from '$lib/utils/format';
	import { pagingParams, type Paging } from '$lib/utils/paging';
	import type { PageData } from './$types';
	import SnapshotChanges from './SnapshotChanges.svelte';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/snapshots')!;
	const base = resolve('/snapshots');
	const hrefFor = (p: Paging) => {
		const q = new URLSearchParams(pagingParams(p)).toString();
		return q ? `${base}?${q}` : base;
	};

	// The latest version, to mark its row (text badge, not colour alone).
	let latestVersion = $state<number | null>(null);
	$effect(() => {
		let live = true;
		data.latest.then((r) => {
			if (live) latestVersion = r.ok ? r.data.version : null;
		});
		return () => {
			live = false;
		};
	});

	/** `sha256:5e88489c3b1a...` -> `sha256:5e88489c3b1a` (full value in the title and the copy). */
	const shortSum = (s: Snapshot['checksum']) => s.slice(0, 'sha256:'.length + 12);
	const entryHref = (changeId: string) =>
		resolve('/(app)/change-log/[change_id]', { change_id: changeId });
</script>

<PageHeader title={section.label} summary={section.summary} />

<section class="latest" aria-labelledby="latest-heading">
	<h2 id="latest-heading">Latest snapshot</h2>
	<AsyncView
		result={data.latest}
		noun="the latest snapshot"
		onretry={invalidateAll}
		headingLevel={3}
	>
		{#snippet children(s)}
			<p class="lead">
				<StatusBadge tone="ok" icon="check" label="Latest" />
				<strong>Snapshot {s.version}</strong>
				{s.summary ?? ''}
			</p>
			<dl class="facts">
				<div>
					<dt>Published</dt>
					<dd>{formatUtc(s.created_at)} by {s.author}</dd>
				</div>
				<div>
					<dt>Checksum</dt>
					<dd>
						<code class="id" title={s.checksum}>{shortSum(s.checksum)}</code>
						<CopyButton value={s.checksum} label="Copy checksum of snapshot {s.version}" />
					</dd>
				</div>
				<div>
					<dt>Size</dt>
					<dd>{formatBytes(s.size_bytes)}</dd>
				</div>
				<div>
					<dt>Change</dt>
					<dd>
						{#if s.change_id}<a href={entryHref(s.change_id)}
								><code class="id">{s.change_id}</code></a
							>{:else}-{/if}
					</dd>
				</div>
			</dl>
			<p class="meta">Gateway nodes load this snapshot within about 10 s of its publish.</p>
		{/snippet}
	</AsyncView>
</section>

<ListView
	result={data.list}
	noun="snapshots"
	caption="Registry snapshots, newest first"
	emptyTitle="No snapshots published yet"
	columns={[
		{ label: 'Version', numeric: true },
		{ label: 'Summary' },
		{ label: 'Changes' },
		{ label: 'Author' },
		{ label: 'Change' },
		{ label: 'Checksum' },
		{ label: 'Size', numeric: true },
		{ label: 'Created' }
	]}
	onretry={invalidateAll}
>
	{#snippet row(s)}
		{@const isLatest = s.version === latestVersion}
		<th scope="row" class="numeric" class:is-latest={isLatest}>
			{#if isLatest}<StatusBadge tone="ok" icon="check" label="Latest" />{/if}
			{s.version}
		</th>
		<td class:is-latest={isLatest}>{s.summary ?? '-'}</td>
		<td class="changes"><SnapshotChanges version={s.version} /></td>
		<td>{s.author}</td>
		<td>
			{#if s.change_id}<a href={entryHref(s.change_id)}><code class="id">{s.change_id}</code></a
				>{:else}-{/if}
		</td>
		<td>
			<code class="id" title={s.checksum}>{shortSum(s.checksum)}</code>
			<CopyButton value={s.checksum} label="Copy checksum of snapshot {s.version}" />
		</td>
		<td class="numeric">{formatBytes(s.size_bytes)}</td>
		<td>{formatUtc(s.created_at)}</td>
	{/snippet}
	{#snippet empty()}
		<p>The first publish of any registry object creates snapshot 1.</p>
	{/snippet}
	{#snippet footer(page)}
		<Pager
			paging={data.paging}
			nextCursor={page.next_cursor}
			count={page.items.length}
			noun="snapshots"
			href={hrefFor}
		/>
	{/snippet}
</ListView>

<style>
	.latest {
		max-width: 72rem;
		margin-bottom: var(--space-4);
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--status-ok);
		border-left-width: 4px;
		border-radius: var(--radius-md);
		background: var(--surface);
	}
	.latest h2 {
		font-size: var(--text-md);
		margin-bottom: var(--space-2);
	}
	.lead {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin: 0 0 var(--space-2);
	}
	.meta {
		margin: var(--space-2) 0 0;
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.is-latest {
		font-weight: 700;
	}
	th.is-latest {
		box-shadow: inset 4px 0 0 var(--status-ok);
	}
	.changes {
		vertical-align: top;
		padding-top: 0.35rem;
		padding-bottom: 0.35rem;
	}
</style>

<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import SpecView from '$lib/components/integration/SpecView.svelte';
	import { integrationStatus } from '$lib/status';
	import { formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const id = $derived(data.integrationId);
	const versionHref = (v: number) =>
		resolve('/(app)/integrations/[integration_id]/versions/[version]', {
			integration_id: id,
			version: String(v)
		});
</script>

<PageHeader title={id} />

<AsyncView result={data.integration} noun="integration {id}" onretry={invalidateAll}>
	{#snippet children(i)}
		{@const s = integrationStatus(i)}
		<section class="head" aria-label="Summary">
			<div class="status-line">
				<StatusBadge tone={s.tone} icon={s.icon} label={s.label} />
				{#if i.description}<p class="desc">{i.description}</p>{/if}
			</div>
			{#if i.paused && i.paused_reason}
				<p class="paused" role="note">Paused: {i.paused_reason}</p>
			{/if}
			<dl class="facts">
				<div>
					<dt>Owner tenant</dt>
					<dd>{i.owner_tenant}</dd>
				</div>
				<div>
					<dt>Capability</dt>
					<dd><code>{i.capability}</code></dd>
				</div>
				<div>
					<dt>Active version</dt>
					<dd>{i.active_version == null ? 'None' : `v${i.active_version}`}</dd>
				</div>
				<div>
					<dt>Latest version</dt>
					<dd>
						{i.latest_version == null ? 'None' : `v${i.latest_version}`}{i.has_draft
							? ' (draft)'
							: ''}
					</dd>
				</div>
				<div>
					<dt>Other tenants allowed</dt>
					<dd>{i.grants.length ? i.grants.join(', ') : 'None'}</dd>
				</div>
				<div>
					<dt>Moves money or quota</dt>
					<dd>{i.moves_money_or_quota ? 'Yes: callers must send an Idempotency-Key' : 'No'}</dd>
				</div>
				<div>
					<dt>Created</dt>
					<dd>{formatUtc(i.created_at)}</dd>
				</div>
				<div>
					<dt>Updated</dt>
					<dd>{formatUtc(i.updated_at)}</dd>
				</div>
			</dl>
			<RoleGate access={data.access}>
				<!-- Publish, rollback, weights and pause controls arrive with HUB-259/260, here. -->
				{#snippet denied(role)}
					<p class="readonly" role="note">
						Read only. Publishing, rolling back, setting weights and pausing need the {role} role.
					</p>
				{/snippet}
			</RoleGate>
		</section>

		<section class="block" aria-labelledby="active-heading">
			{#if i.active}
				{@const a = i.active}
				<h2 id="active-heading">Active version v{a.version}</h2>
				<p class="meta">
					Published {formatUtc(a.published_at)} by {a.published_by ?? a.author}. Reason: {a.reason}.
					{#if a.snapshot_version != null}Snapshot {a.snapshot_version}.{/if}
				</p>
				<SpecView spec={a.spec} />
			{:else}
				<h2 id="active-heading">Active version</h2>
				<p class="meta">None. Invocations answer 503 until a first version is published.</p>
			{/if}
		</section>
	{/snippet}
</AsyncView>

<section class="block" aria-labelledby="history-heading">
	<h2 id="history-heading">Version history</h2>
	<p class="meta">
		Every version, newest first. Rollback publishes a copy, so nothing is ever removed.
	</p>
	<ListView
		result={data.versions}
		noun="versions"
		caption="Versions of {id}, newest first"
		emptyTitle="No versions yet"
		columns={[
			{ label: 'Version', numeric: true },
			{ label: 'State' },
			{ label: 'Author' },
			{ label: 'Reason' },
			{ label: 'Created' },
			{ label: 'Published' },
			{ label: 'Based on', numeric: true }
		]}
		onretry={invalidateAll}
	>
		{#snippet row(v)}
			<th scope="row" class="numeric" class:active={v.is_active}>
				<a href={versionHref(v.version)}>v{v.version}</a>
			</th>
			<td>
				{#if v.is_active}
					<StatusBadge tone="ok" icon="check" label="Active" />
				{:else if v.state === 'draft'}
					<StatusBadge tone="warn" icon="changelog" label="Draft" />
				{:else}
					<span class="muted">Published</span>
				{/if}
			</td>
			<td>{v.author}</td>
			<td class="reason" title={v.reason}>{v.reason}</td>
			<td>{formatUtc(v.created_at)}</td>
			<td>{v.published_at ? formatUtc(v.published_at) : '-'}</td>
			<td class="numeric">{v.based_on == null ? '-' : `v${v.based_on}`}</td>
		{/snippet}
		{#snippet empty()}<p>Create a draft with hubctl or the admin API.</p>{/snippet}
		{#snippet footer(page)}
			<p class="meta" role="status">
				{page.items.length}
				{page.items.length === 1 ? 'version' : 'versions'}.
				{#if page.next_cursor}Showing the newest {page.items.length}; older versions are not loaded.{/if}
			</p>
		{/snippet}
	</ListView>
</section>

<style>
	.head {
		display: grid;
		gap: var(--space-3);
		max-width: 72rem;
		padding-bottom: var(--space-4);
		border-bottom: 1px solid var(--rule);
	}
	.status-line {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-3);
	}
	.desc {
		margin: 0;
		color: var(--ink-muted);
	}
	.paused {
		margin: 0;
		padding: var(--space-2) var(--space-3);
		border-left: 4px solid var(--status-neutral);
		background: var(--status-neutral-wash);
		font-size: var(--text-sm);
	}
	.readonly {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.block {
		margin-top: var(--space-5);
		max-width: 72rem;
	}
	.block h2 {
		font-size: var(--text-lg);
	}
	.meta {
		margin: var(--space-1) 0 var(--space-3);
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.active a {
		font-weight: 700;
	}
	.reason {
		max-width: 22rem;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>

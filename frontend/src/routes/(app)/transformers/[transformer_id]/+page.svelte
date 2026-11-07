<script lang="ts">
	import { onMount } from 'svelte';
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import ChangeNotice from '$lib/components/ChangeNotice.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import { takeFlash, type Flash } from '$lib/stores/flash.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import VersionView from '$lib/components/transformer/VersionView.svelte';
	import { formatNumber, formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';

	/**
	 * Transformer (HUB-268): header, the latest version in full (source, fixtures, goldens) and the
	 * version history. Older versions open on their own page.
	 */
	let { data }: { data: PageData } = $props();

	const id = $derived(data.transformerId);
	const scoped = $derived(data.access.tenantScoped && data.access.known);
	let notice = $state.raw<Flash | null>(null);
	onMount(() => {
		notice = takeFlash();
	});
</script>

<PageHeader title={id} summary="The transformer, its latest version and every earlier one." />

{#if notice}
	<ChangeNotice {...notice} ondismiss={() => (notice = null)} />
{/if}

{#if scoped}
	<p class="note" role="note">
		Transformers map to vendor APIs, which tenant viewers cannot see. Ask the platform team about a
		mapping.
	</p>
{:else}
	<AsyncView result={data.transformer} noun="transformer {id}" onretry={invalidateAll}>
		{#snippet children(t)}
			<section class="head" aria-label="Transformer summary">
				<dl class="facts">
					<div>
						<dt>Kind</dt>
						<dd>{t.kind}</dd>
					</div>
					<div>
						<dt>Capability</dt>
						<dd><code>{t.capability}</code></dd>
					</div>
					<div>
						<dt>Vendor endpoint</dt>
						<dd><code>{t.vendor_endpoint}</code></dd>
					</div>
					<div>
						<dt>Latest version</dt>
						<dd>v{t.latest_version}</dd>
					</div>
					<div>
						<dt>Used by active integration versions</dt>
						<dd>{formatNumber(t.used_by_active_versions)}</dd>
					</div>
					<div>
						<dt>Updated</dt>
						<dd>{formatUtc(t.updated_at)}</dd>
					</div>
				</dl>
			</section>
			<RoleGate access={data.access} ops={['createTransformerVersion', 'runTransformerGoldens']}>
				<p class="new-version">
					<a
						class="btn btn-primary"
						href={resolve('/(app)/transformers/[transformer_id]/new-version', {
							transformer_id: id
						})}>New version (v{t.latest_version + 1})</a
					>
				</p>
			</RoleGate>
			<section class="block" aria-labelledby="latest-heading">
				<h2 id="latest-heading">Latest version: v{t.latest.version}</h2>
				<VersionView version={t.latest} access={data.access} />
			</section>
		{/snippet}
	</AsyncView>

	<section class="block" aria-labelledby="versions-heading">
		<h2 id="versions-heading">Versions</h2>
		<AsyncView result={data.versions} noun="the versions" onretry={invalidateAll} headingLevel={3}>
			{#snippet children(list)}
				{#if list.items.length === 0}
					<p class="muted">No versions returned.</p>
				{:else}
					<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
					<div class="table-scroll" role="region" aria-label="Version history" tabindex="0">
						<table>
							<caption class="visually-hidden">Versions of {id}, newest first</caption>
							<thead>
								<tr>
									<th scope="col">Version</th>
									<th scope="col">Goldens</th>
									<th scope="col">Author</th>
									<th scope="col">Reason</th>
									<th scope="col">Created</th>
								</tr>
							</thead>
							<tbody>
								{#each list.items as v (v.version)}
									<tr>
										<th scope="row">
											<a
												href={resolve('/(app)/transformers/[transformer_id]/versions/[version]', {
													transformer_id: id,
													version: String(v.version)
												})}><code>{v.ref}</code></a
											>
										</th>
										<td>
											<StatusBadge
												tone={v.goldens_passed ? 'ok' : 'danger'}
												icon={v.goldens_passed ? 'closed' : 'open'}
												label={v.goldens_passed ? 'Passing' : 'Failing'}
											/>
										</td>
										<td>{v.author}</td>
										<td class="reason">{v.reason}</td>
										<td>{formatUtc(v.created_at)}</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{/if}
			{/snippet}
		</AsyncView>
	</section>
{/if}

<style>
	.new-version {
		margin: var(--space-4) 0 0;
	}
	.note {
		color: var(--ink-muted);
		max-width: 46rem;
	}
	.head {
		max-width: 72rem;
		padding-bottom: var(--space-4);
		border-bottom: 1px solid var(--rule);
	}
	.block {
		margin-top: var(--space-5);
		max-width: 72rem;
	}
	.block > h2 {
		font-size: var(--text-xl);
		margin-bottom: var(--space-3);
	}
	.table-scroll {
		overflow-x: auto;
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: var(--text-sm);
	}
	th,
	td {
		height: var(--row-height);
		padding: 0 var(--space-3);
		text-align: left;
		white-space: nowrap;
		border-bottom: 1px solid var(--rule);
	}
	thead th {
		background: var(--surface-sunken);
		color: var(--ink-muted);
		font-size: var(--text-xs);
		font-weight: 600;
	}
	.reason {
		white-space: normal;
		min-width: 16rem;
	}
</style>

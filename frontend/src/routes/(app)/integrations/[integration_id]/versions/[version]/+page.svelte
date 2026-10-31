<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import SpecView from '$lib/components/integration/SpecView.svelte';
	import { formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
	const id = $derived(data.integrationId);
</script>

<PageHeader title="{id} v{data.versionNumber}" />

<AsyncView
	result={data.version}
	noun="version {data.versionNumber} of {id}"
	onretry={invalidateAll}
>
	{#snippet children(v)}
		<section class="head" aria-label="Version summary">
			<div>
				{#if v.state === 'draft'}
					<StatusBadge tone="warn" icon="changelog" label="Draft" />
				{:else}
					<StatusBadge tone="neutral" icon="check" label="Published" />
				{/if}
			</div>
			<dl class="facts">
				<div>
					<dt>Author</dt>
					<dd>{v.author}</dd>
				</div>
				<div>
					<dt>Reason</dt>
					<dd>{v.reason}</dd>
				</div>
				<div>
					<dt>Created</dt>
					<dd>{formatUtc(v.created_at)}</dd>
				</div>
				<div>
					<dt>Published</dt>
					<dd>
						{v.published_at
							? `${formatUtc(v.published_at)} by ${v.published_by ?? v.author}`
							: 'Not published'}
					</dd>
				</div>
				<div>
					<dt>Based on</dt>
					<dd>{v.based_on == null ? 'None' : `v${v.based_on}`}</dd>
				</div>
				{#if v.copied_from_version != null}
					<div>
						<dt>Copy of</dt>
						<dd>v{v.copied_from_version} (rollback or weights change)</dd>
					</div>
				{/if}
				<div>
					<dt>Snapshot</dt>
					<dd>{v.snapshot_version == null ? 'None' : v.snapshot_version}</dd>
				</div>
			</dl>
		</section>
		<section class="block" aria-labelledby="spec-heading">
			<h2 id="spec-heading">Spec</h2>
			<SpecView spec={v.spec} />
		</section>
	{/snippet}
</AsyncView>

<p class="back">
	<a href={resolve('/(app)/integrations/[integration_id]', { integration_id: id })}
		>All versions of {id}</a
	>
</p>

<style>
	.head {
		display: grid;
		gap: var(--space-3);
		max-width: 72rem;
		padding-bottom: var(--space-4);
		border-bottom: 1px solid var(--rule);
	}
	.block {
		margin-top: var(--space-5);
		max-width: 72rem;
	}
	.block h2 {
		margin-bottom: var(--space-3);
		font-size: var(--text-lg);
	}
	.back {
		margin-top: var(--space-5);
		font-weight: 600;
	}
</style>

<script lang="ts">
	import { onMount } from 'svelte';
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import ChangeNotice from '$lib/components/ChangeNotice.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import SchemaFields from '$lib/components/schema/SchemaFields.svelte';
	import { takeFlash, type Flash } from '$lib/stores/flash.svelte';
	import { formatDurationMs, formatNumber, formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';
	import { REQUEST_PARTS } from '../../../endpoint-form';

	/**
	 * One endpoint version (HUB-290, HUB-436): the call, the request it accepts part by part (path
	 * parameters, query parameters, headers, body; what request transformer samples are checked
	 * against) and the informational response schema. Versions never change; the latest of a name
	 * offers "New version", which creates `<name>@<n+1>` from these values on its own page.
	 */
	let { data }: { data: PageData } = $props();
	const ref = $derived(data.endpointRef);
	const vendorHref = $derived(resolve('/(app)/vendors/[vendor_id]', { vendor_id: data.vendorId }));

	let notice = $state.raw<Flash | null>(null);
	onMount(() => {
		notice = takeFlash();
	});
	const partLabels = {
		path: 'Path parameters',
		query: 'Query parameters',
		headers: 'Headers',
		body: 'Body'
	} as const;
</script>

<PageHeader title="{data.vendorId}.{ref}" summary="One immutable endpoint version." />

{#if notice}
	<ChangeNotice {...notice} ondismiss={() => (notice = null)} />
{/if}

{#if data.endpoint === null}
	<p class="fk-note" role="note">
		Vendors are internal to the platform team; tenant viewers cannot see them.
	</p>
{:else}
	<AsyncView result={data.endpoint} noun="endpoint {ref}" onretry={invalidateAll}>
		{#snippet children({ endpoint: e, latest })}
			<dl class="facts">
				<div>
					<dt>Vendor</dt>
					<dd><a href={vendorHref}><code>{data.vendorId}</code></a></dd>
				</div>
				<div>
					<dt>Call</dt>
					<dd><code>{e.method} {e.path}</code></dd>
				</div>
				<div>
					<dt>Timeout</dt>
					<dd>
						{formatDurationMs(e.timeout_ms)} (connect {formatDurationMs(e.connect_timeout_ms)})
					</dd>
				</div>
				<div>
					<dt>Idempotent</dt>
					<dd>{e.idempotent ? 'Yes: safe to retry and fail over' : 'No'}</dd>
				</div>
				<div>
					<dt>Batch size limit</dt>
					<dd>{e.batch_max == null ? 'No batching' : formatNumber(e.batch_max)}</dd>
				</div>
				<div>
					<dt>Streams</dt>
					<dd>{e.stream ? 'Yes' : 'No'}</dd>
				</div>
				<div>
					<dt>Used by transformers as</dt>
					<dd><code>{data.vendorId}.{e.endpoint_ref}</code></dd>
				</div>
				<div>
					<dt>Created</dt>
					<dd>{formatUtc(e.created_at)}</dd>
				</div>
			</dl>
			{#if latest && latest.version !== e.version}
				<p class="fk-note" role="note">
					A newer version exists: <a
						href={resolve('/(app)/vendors/[vendor_id]/endpoints/[endpoint_ref]', {
							vendor_id: data.vendorId,
							endpoint_ref: latest.endpoint_ref
						})}><code>{latest.endpoint_ref}</code></a
					>.
				</p>
			{:else if latest}
				<RoleGate access={data.access} op="createVendorEndpoint">
					<div class="fk-actions actions">
						<a
							class="btn btn-primary"
							href="{resolve('/(app)/vendors/[vendor_id]/endpoints/new', {
								vendor_id: data.vendorId
							})}?from={encodeURIComponent(e.name)}">New version ({e.name}@{e.version + 1})</a
						>
					</div>
				</RoleGate>
			{/if}

			<section class="block" aria-labelledby="req-h">
				<h2 id="req-h">Request it accepts</h2>
				<p class="fk-note">
					Request transformer samples must produce a request that fits this. Live calls are never
					checked against it.
				</p>
				{#each REQUEST_PARTS as part (part)}
					<h3>{partLabels[part]}</h3>
					{#if e.request_schema?.[part] === undefined}
						<p class="muted none">Not sent.</p>
					{:else}
						<SchemaFields
							schema={e.request_schema[part]}
							label="{partLabels[part]} of {e.endpoint_ref}"
							empty="Declared, without fields."
						/>
					{/if}
				{/each}
			</section>

			<section class="block" aria-labelledby="res-h">
				<h2 id="res-h">Response <span class="muted">(informational, never enforced)</span></h2>
				{#if e.response_schema}
					<SchemaFields
						schema={e.response_schema}
						label="Response of {e.endpoint_ref}"
						empty="Declared, without fields."
					/>
				{:else}
					<p class="muted none">Not documented.</p>
				{/if}
			</section>
		{/snippet}
	</AsyncView>
{/if}

<style>
	.facts {
		max-width: 72rem;
	}
	.actions {
		margin-top: var(--space-4);
	}
	.block {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: var(--space-2);
		margin-top: var(--space-5);
		max-width: 72rem;
	}
	.block h2 {
		font-size: var(--text-lg);
	}
	.block h3 {
		margin: var(--space-2) 0 0;
		font-size: var(--text-sm);
	}
	.none {
		margin: 0;
		font-size: var(--text-sm);
	}
</style>

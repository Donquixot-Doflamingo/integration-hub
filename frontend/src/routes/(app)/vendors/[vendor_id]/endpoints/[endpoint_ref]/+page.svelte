<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { VendorEndpointChange } from '$lib/api/types';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import ChangeNotice from '$lib/components/ChangeNotice.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import type { Flash } from '$lib/stores/flash.svelte';
	import { formatDurationMs, formatNumber, formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';
	import EndpointDialog from '../../../EndpointDialog.svelte';

	/**
	 * One endpoint version (HUB-290). Versions never change; the latest of a name offers "New
	 * version", which creates `<name>@<n+1>` from these values.
	 */
	let { data }: { data: PageData } = $props();
	const ref = $derived(data.endpointRef);
	const vendorHref = $derived(resolve('/(app)/vendors/[vendor_id]', { vendor_id: data.vendorId }));

	let open = $state(false);
	let notice = $state.raw<Flash | null>(null);
	function onCreated(d: ActionSuccessData) {
		const e = (d.result as VendorEndpointChange).endpoint;
		notice = {
			message: `Created endpoint ${e.endpoint_ref}. ${ref} is unchanged; transformers move to ${e.endpoint_ref} with their next version.`,
			changeId: d.changeId,
			requestId: d.requestId,
			link: {
				href: resolve('/(app)/vendors/[vendor_id]/endpoints/[endpoint_ref]', {
					vendor_id: data.vendorId,
					endpoint_ref: e.endpoint_ref
				}),
				label: `Open ${e.endpoint_ref}`
			}
		};
	}
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
						<button type="button" class="btn btn-primary" onclick={() => (open = true)}>
							New version ({e.name}@{e.version + 1})
						</button>
					</div>
				</RoleGate>
				<EndpointDialog bind:open vendorId={data.vendorId} from={latest} onsuccess={onCreated} />
			{/if}
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
</style>

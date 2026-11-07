<script lang="ts">
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { VendorEndpointChange } from '$lib/api/types';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import { setFlash } from '$lib/stores/flash.svelte';
	import type { PageData } from './$types';
	import EndpointForm from '../../../EndpointForm.svelte';

	/**
	 * Add an endpoint to a vendor, or the next version of one (`?from=<name>`), with the request it
	 * accepts (HUB-290, HUB-436, `createVendorEndpoint`, admin).
	 */
	let { data }: { data: PageData } = $props();
	const id = $derived(data.vendorId);
	const vendorHref = $derived(resolve('/(app)/vendors/[vendor_id]', { vendor_id: id }));

	async function onsuccess(d: ActionSuccessData) {
		const e = (d.result as VendorEndpointChange).endpoint;
		setFlash({
			message: `Created endpoint ${e.endpoint_ref}: ${e.method} ${e.path}. Point a request transformer at ${id}.${e.endpoint_ref} to use it.`,
			changeId: d.changeId,
			requestId: d.requestId
		});
		await goto(
			resolve('/(app)/vendors/[vendor_id]/endpoints/[endpoint_ref]', {
				vendor_id: id,
				endpoint_ref: e.endpoint_ref
			})
		);
	}
</script>

<PageHeader
	title={data.fromName ? `New version of ${id}.${data.fromName}` : `Add an endpoint to ${id}`}
	summary="One vendor API call: method, path, timeouts and the request it accepts, part by part."
/>

<RoleGate access={data.access} op="createVendorEndpoint">
	<AsyncView result={data.start} noun="vendor {id}" onretry={invalidateAll}>
		{#snippet children(s)}
			{#if data.fromName && !s.from}
				<p class="fk-note" role="note">
					{id} has no endpoint named {data.fromName}; this adds a new one.
				</p>
			{/if}
			{#key s.from?.endpoint_ref}
				<EndpointForm vendorId={id} from={s.from} {onsuccess} />
			{/key}
		{/snippet}
	</AsyncView>
	{#snippet denied(role)}
		<p class="fk-note" role="note">
			Read only. Adding endpoints needs the {role} role. <a href={vendorHref}>Back to {id}</a>
		</p>
	{/snippet}
</RoleGate>

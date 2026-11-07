<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import type { PageData } from './$types';
	import VendorEditor from './VendorEditor.svelte';

	/** Edit a vendor (HUB-290, `updateVendor`, admin): PUT with the loaded ETag as If-Match. */
	let { data }: { data: PageData } = $props();
	const id = $derived(data.vendorId);
</script>

<PageHeader title="Edit {id}" />

<RoleGate access={data.access} op="updateVendor">
	<AsyncView result={data.vendor} noun="vendor {id}" onretry={invalidateAll}>
		{#snippet children(d)}
			{#key d.vendor.revision}
				<VendorEditor vendor={d.vendor} etag={d.etag} />
			{/key}
		{/snippet}
	</AsyncView>
	{#snippet denied(role)}
		<p class="fk-note" role="note">
			Read only. Editing vendors needs the {role} role.
			<a href={resolve('/(app)/vendors/[vendor_id]', { vendor_id: id })}>Back to {id}</a>
		</p>
	{/snippet}
</RoleGate>

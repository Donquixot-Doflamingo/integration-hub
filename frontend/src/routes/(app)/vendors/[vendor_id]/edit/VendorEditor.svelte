<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { Vendor, VendorChange } from '$lib/api/types';
	import AuthoringForm from '$lib/components/AuthoringForm.svelte';
	import { setFlash } from '$lib/stores/flash.svelte';
	import { blockersFrom } from '$lib/utils/form';
	import VendorFields from '../../VendorFields.svelte';
	import { hasChanges, problems, valuesFromVendor } from '../../vendor-form';

	/**
	 * The edit form for one loaded vendor (HUB-290). It keeps the values the page opened with
	 * (`before`, also posted so the server can refuse a no-op) and the ETag for If-Match.
	 */
	let { vendor, etag }: { vendor: Vendor; etag: string | null } = $props();

	const before = untrack(() => valuesFromVendor(vendor));
	let values = $state(structuredClone(before));
	const changed = $derived(hasChanges(values, before));
	const blockers = $derived([
		...blockersFrom(problems(values, 'edit')),
		...(changed ? [] : [{ message: 'Nothing changed yet. Edit at least one field.' }])
	]);
	const refs = $derived(vendor.endpoints.map((e) => e.endpoint_ref));

	async function onsuccess(d: ActionSuccessData) {
		const v = (d.result as VendorChange).vendor;
		setFlash({
			message: `Saved vendor ${v.vendor_id}: revision ${v.revision}. Integrations pick the change up on their next publish or snapshot.`,
			changeId: d.changeId,
			requestId: d.requestId
		});
		await goto(resolve('/(app)/vendors/[vendor_id]', { vendor_id: v.vendor_id }), {
			invalidateAll: true
		});
	}
</script>

<p class="fk-note">
	Editing revision {vendor.revision}. Saving replaces the whole definition below (regions, auth
	scheme, defaults, webhook verification); endpoints are changed on the vendor page.
</p>
<AuthoringForm
	action="?/update"
	verb="Save vendor"
	busyVerb="Saving..."
	subject={vendor.vendor_id}
	{blockers}
	{onsuccess}
>
	{#snippet children(errors, attempted)}
		<input type="hidden" name="if_match" value={etag ?? ''} />
		<input type="hidden" name="before" value={JSON.stringify(before)} />
		<VendorFields mode="edit" bind:values {errors} showAll={attempted} endpointRefs={refs} />
	{/snippet}
</AuthoringForm>

<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { VendorChange } from '$lib/api/types';
	import AuthoringForm from '$lib/components/AuthoringForm.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import { setFlash } from '$lib/stores/flash.svelte';
	import { blockersFrom } from '$lib/utils/form';
	import type { PageData } from './$types';
	import VendorFields from '../VendorFields.svelte';
	import { emptyValues, problems } from '../vendor-form';

	/** Create a vendor (HUB-290, `createVendor`, admin). Endpoints are added on its page next. */
	let { data }: { data: PageData } = $props();

	let values = $state(emptyValues());
	const blockers = $derived(blockersFrom(problems(values, 'create')));

	async function onsuccess(d: ActionSuccessData) {
		const v = (d.result as VendorChange).vendor;
		const href = resolve('/(app)/vendors/[vendor_id]', { vendor_id: v.vendor_id });
		setFlash({
			message: `Created vendor ${v.vendor_id} (${v.display_name}), revision ${v.revision}. Next: add its endpoints, then its accounts.`,
			changeId: d.changeId,
			requestId: d.requestId
		});
		await goto(href);
	}
</script>

<PageHeader
	title="Create vendor"
	summary="A vendor is an external API the Hub calls: where it lives per region, how it authenticates and its defaults."
/>

<RoleGate access={data.access} op="createVendor">
	<AuthoringForm
		action="?/create"
		verb="Create vendor"
		busyVerb="Creating..."
		subject="the vendor list"
		conflictIsStale={false}
		{blockers}
		{onsuccess}
	>
		{#snippet children(errors, attempted)}
			<VendorFields mode="create" bind:values {errors} showAll={attempted} />
		{/snippet}
	</AuthoringForm>
	{#snippet denied(role)}
		<p class="fk-note" role="note">
			Read only. Creating vendors needs the {role} role.
			<a href={resolve('/vendors')}>Back to vendors</a>
		</p>
	{/snippet}
</RoleGate>

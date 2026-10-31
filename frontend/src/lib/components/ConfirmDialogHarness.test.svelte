<script lang="ts">
	import type { ActionSuccessData } from '$lib/api/actions';
	import ConfirmDialog from './ConfirmDialog.svelte';

	let {
		confirmText,
		onsuccess
	}: { confirmText?: string; onsuccess?: (d: ActionSuccessData) => void } = $props();
	let open = $state(false);
</script>

<button type="button" onclick={() => (open = true)}>Roll back to v7</button>
<ConfirmDialog
	bind:open
	title="Roll back int_evt_brandA to v7"
	verb="Roll back"
	busyVerb="Rolling back..."
	action="?/go"
	subject="int_evt_brandA"
	{confirmText}
	{onsuccess}
>
	<p>Creates v9 with the content of v7.</p>
	{#snippet fields()}<input type="hidden" name="to_version" value="7" />{/snippet}
</ConfirmDialog>

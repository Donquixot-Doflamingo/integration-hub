<script lang="ts">
	import Icon from './Icon.svelte';

	/**
	 * The problem of one form field (HUB-290+): rendered under the input, referenced by the input's
	 * `aria-describedby`. Renders nothing when there is no message.
	 */
	let { id, message }: { id: string; message: string | null | undefined } = $props();
</script>

{#if message}
	<p {id} class="fk-error"><Icon name="alert" size={14} />{message}</p>
{/if}

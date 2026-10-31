<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { HubApiError } from '$lib/api/errors';
	import ErrorPanel from './ErrorPanel.svelte';
	import PageHeader from './PageHeader.svelte';

	/**
	 * Body of `+error.svelte` pages: errors thrown from a load (`error(404, {...})`), unknown routes
	 * and unexpected failures, shown with the shared `ErrorPanel` (HUB-255). Never a stack trace.
	 */
	let {
		home = false
	}: { /** Offer a link back to the console (outside the shell). */ home?: boolean } = $props();

	const notFound = $derived(page.status === 404);
	const error = $derived(
		new HubApiError({
			status: page.status,
			code: page.error?.code ?? (notFound ? 'NOT_FOUND' : 'INTERNAL_ERROR'),
			message: page.error?.message ?? 'Something went wrong.',
			requestId: page.error?.requestId ?? null,
			retryable: page.status >= 500
		})
	);
</script>

<PageHeader title={notFound ? 'Not found' : 'Something went wrong'} />
<ErrorPanel
	{error}
	context={notFound ? 'Nothing lives at this address' : 'The console hit an error'}
	onretry={page.status >= 500 ? invalidateAll : undefined}
/>
{#if home}<p class="home"><a href={resolve('/')}>Go to the console home</a></p>{/if}

<style>
	.home {
		margin-top: var(--space-4);
		font-weight: 600;
	}
</style>

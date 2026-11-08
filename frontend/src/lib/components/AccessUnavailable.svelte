<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import type { HubApiError } from '$lib/api/errors';
	import ErrorPanel from './ErrorPanel.svelte';
	import PageHeader from './PageHeader.svelte';

	/**
	 * Shown in place of a page when `/me` failed with anything but 401 (HUB-455). Without `/me` the
	 * console cannot tell an admin from a tenant viewer, so it must not guess: it says so and
	 * retries. The layout load re-runs with `invalidateAll`, so one successful retry fixes every page.
	 */
	let { error }: { error: HubApiError } = $props();
</script>

<PageHeader
	title="Your access could not be loaded"
	summary="The console reads your role from the admin API. Until it answers, nothing role-dependent is shown."
/>
<ErrorPanel {error} context="Could not load your access" onretry={invalidateAll} />

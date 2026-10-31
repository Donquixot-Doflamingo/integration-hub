<script lang="ts">
	import ErrorPage from '$lib/components/ErrorPage.svelte';
</script>

<!-- Outside the shell (unknown top-level paths): same panel, plus a way back. -->
<main class="bare">
	<ErrorPage home />
</main>

<style>
	.bare {
		max-width: 50rem;
		margin: 0 auto;
		padding: 10vh var(--space-4) var(--space-6);
	}
</style>

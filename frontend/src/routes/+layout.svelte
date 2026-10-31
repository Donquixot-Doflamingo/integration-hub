<script lang="ts">
	import '@fontsource-variable/atkinson-hyperlegible-next';
	import '@fontsource-variable/atkinson-hyperlegible-mono';
	import '../app.css';
	import type { Snippet } from 'svelte';
	import { themeState } from '$lib/stores/theme.svelte';

	let { children }: { children: Snippet } = $props();

	// Follow the stored preference and OS changes; reflect it on <html data-theme>.
	$effect(() => themeState.init());
	$effect(() => {
		document.documentElement.dataset.theme = themeState.theme;
	});
</script>

{@render children()}

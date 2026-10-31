<script lang="ts">
	import { icons, type IconName } from './icons';

	let {
		name,
		size = 20,
		label
	}: {
		name: IconName;
		size?: number;
		/** Accessible name. Omit for decorative icons next to visible text. */
		label?: string;
	} = $props();
</script>

<svg
	class="icon"
	width={size}
	height={size}
	viewBox="0 0 20 20"
	fill="none"
	stroke="currentColor"
	stroke-width="1.6"
	stroke-linecap="round"
	stroke-linejoin="round"
	role={label ? 'img' : undefined}
	aria-label={label}
	aria-hidden={label ? undefined : 'true'}
	focusable="false"
>
	<path d={icons[name]} />
</svg>

<style>
	.icon {
		flex: none;
		display: block;
	}
</style>

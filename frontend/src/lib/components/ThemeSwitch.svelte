<script lang="ts">
	import { themeState, type ThemePreference } from '$lib/stores/theme.svelte';
	import Icon from './Icon.svelte';
	import type { IconName } from './icons';

	const options: { value: ThemePreference; label: string; icon: IconName }[] = [
		{ value: 'system', label: 'Match system', icon: 'system' },
		{ value: 'light', label: 'Light', icon: 'sun' },
		{ value: 'dark', label: 'Dark', icon: 'moon' }
	];
</script>

<fieldset class="theme">
	<legend class="visually-hidden">Theme</legend>
	{#each options as o (o.value)}
		<label title={o.label}>
			<input
				type="radio"
				name="theme"
				value={o.value}
				checked={themeState.preference === o.value}
				onchange={() => themeState.set(o.value)}
			/>
			<Icon name={o.icon} size={16} />
			<span class="visually-hidden">{o.label}</span>
		</label>
	{/each}
</fieldset>

<style>
	.theme {
		display: inline-flex;
		margin: 0;
		padding: 2px;
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
	}
	label {
		position: relative;
		display: grid;
		place-items: center;
		width: 1.85rem;
		height: 1.6rem;
		border-radius: var(--radius-sm);
		color: var(--ink-muted);
		cursor: pointer;
	}
	input {
		position: absolute;
		inset: 0;
		margin: 0;
		opacity: 0;
		cursor: pointer;
	}
	label:has(input:checked) {
		background: var(--signal-wash);
		color: var(--signal);
		box-shadow: inset 0 0 0 1px var(--signal);
	}
	label:has(input:focus-visible) {
		box-shadow: var(--focus-ring);
	}
</style>

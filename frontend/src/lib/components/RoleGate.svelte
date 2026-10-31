<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { Role } from '$lib/api/types';
	import { hasRole, roleLabels, type Access } from '$lib/auth/roles';

	/**
	 * Shows `children` only when the user holds `min` (default `editor`, the minimum role of every
	 * mutating admin operation except tenants, API keys and breaker force, which need `admin`).
	 * Otherwise renders `denied` (e.g. a disabled control or a read-only note), or nothing.
	 * The API's 403 is still the truth; this only keeps the UI honest about what a role can do.
	 */
	let {
		access,
		min = 'editor',
		children,
		denied
	}: {
		access: Access;
		min?: Role;
		children: Snippet;
		/** Receives the role that would be needed, e.g. to say "Needs the Editor role". */
		denied?: Snippet<[string]>;
	} = $props();

	const allowed = $derived(access.known && hasRole(access.roles, min));
</script>

{#if allowed}
	{@render children()}
{:else}
	{@render denied?.(roleLabels[min])}
{/if}

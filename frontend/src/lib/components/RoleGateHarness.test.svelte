<script lang="ts">
	import type { Role } from '$lib/api/types';
	import type { Access } from '$lib/auth/roles';
	import RoleGate from './RoleGate.svelte';

	let { access, min }: { access: Access; min: Role } = $props();
</script>

<RoleGate {access} {min}>
	<button type="button">Publish</button>
	{#snippet denied(needed)}<p>Read only: needs the {needed} role</p>{/snippet}
</RoleGate>

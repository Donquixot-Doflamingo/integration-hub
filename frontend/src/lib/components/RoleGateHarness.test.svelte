<script lang="ts">
	import type { Role } from '$lib/api/types';
	import type { Access, OperationId } from '$lib/auth/roles';
	import RoleGate from './RoleGate.svelte';

	let {
		access,
		min,
		op,
		ops
	}: { access: Access; min?: Role; op?: OperationId; ops?: OperationId[] } = $props();
</script>

<RoleGate {access} {min} {op} {ops}>
	<button type="button">Publish</button>
	{#snippet denied(needed)}<p>Read only: needs the {needed} role</p>{/snippet}
</RoleGate>

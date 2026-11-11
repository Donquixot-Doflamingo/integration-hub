<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { Role } from '$lib/api/types';
	import {
		hasRole,
		highestRole,
		requiredRole,
		roleLabels,
		type Access,
		type OperationId
	} from '$lib/auth/roles';

	/**
	 * Renders `children` only when the user may call the gated operation(s); otherwise renders the
	 * `denied` snippet (a read-only note), or nothing. Gated controls are never rendered disabled:
	 * a role that cannot do something does not see the control at all (HUB-288).
	 *
	 * Prefer `op` (or `ops`): the minimum role then comes from the contract's `x-required-role` of
	 * that operation (`roles.gen.ts`), so the UI cannot drift from the API. With `ops`, every listed
	 * operation must be allowed (e.g. a count-then-purge flow). `min` remains for gates that are not
	 * about one operation. The API's 403 is still the truth.
	 */
	let {
		access,
		op,
		ops,
		min,
		children,
		denied
	}: {
		access: Access;
		/** The admin operation the gated control calls, e.g. `purgeDeadLetters`. */
		op?: OperationId;
		/** Several operations the gated controls call; all must be allowed. */
		ops?: readonly OperationId[];
		/** Explicit minimum role; defaults to `editor` when neither `op` nor `ops` is given. */
		min?: Role;
		/** Optional: a gate can carry only the `denied` note. */
		children?: Snippet;
		/** Receives the label of the role that would be needed, e.g. "Admin". */
		denied?: Snippet<[string]>;
	} = $props();

	const required = $derived.by<Role[]>(() => {
		const r = [...(op ? [op] : []), ...(ops ?? [])].map(requiredRole).concat(min ?? []);
		return r.length ? r : ['editor'];
	});
	// The roles form a lattice (`tenant_admin` is not above `editor`), so every requirement is
	// checked on its own; the note names the strongest role among the ones that are not met.
	const unmet = $derived(required.filter((r) => !hasRole(access.roles, r)));
	const needed = $derived<Role>(highestRole(unmet.length ? unmet : required) ?? 'editor');
	const allowed = $derived(access.known && unmet.length === 0);
</script>

{#if allowed}
	{@render children?.()}
{:else}
	{@render denied?.(roleLabels[needed])}
{/if}

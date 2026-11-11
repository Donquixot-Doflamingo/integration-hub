<script lang="ts">
	import type { ApiResult } from '$lib/api/client';
	import type { ListPage } from '$lib/api/list';
	import type { RegistrarEndpoint, Tenant } from '$lib/api/types';
	import type { Access } from '$lib/auth/roles';
	import Icon from '$lib/components/Icon.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import { rateLimitText } from '../tenant-form';

	/**
	 * "Complete setup" for a tenant that came from ISFC with `setup: pending` (HUB-485, AUTH_PLAN
	 * §6.3). Until a tenant admin finishes it, callers are refused. The checklist shows what the
	 * sync gave the tenant and what is left (display name, rate limit, callback endpoints); the
	 * button opens the dialog that sets `setup: complete` (updateTenant).
	 */
	let {
		tenant,
		access,
		endpoints,
		onstart
	}: {
		tenant: Tenant;
		access: Access;
		endpoints: Promise<ApiResult<ListPage<RegistrarEndpoint>>>;
		onstart: () => void;
	} = $props();

	// Callback endpoints are optional for setup, so a failed read only hides the count.
	let endpointCount = $state<number | null>(null);
	$effect(() => {
		let live = true;
		endpointCount = null;
		endpoints.then((r) => {
			if (live) endpointCount = r.ok ? r.data.items.length : null;
		});
		return () => {
			live = false;
		};
	});

	const fromIsfc = $derived(tenant.isfc !== null && tenant.display_name === tenant.isfc.name);
</script>

<section class="setup" aria-labelledby="setup-heading">
	<h2 id="setup-heading"><Icon name="alert" size={18} />Finish setting up this tenant</h2>
	<p class="lede">
		ISFC created this tenant. Callers are refused until setup is complete. Check these, then
		complete setup.
	</p>
	<ol>
		<li>
			<strong>Display name:</strong>
			{tenant.display_name}
			<span class="hint"
				>{fromIsfc ? '(the ISFC name; set your own if you prefer)' : '(set in the Hub)'}</span
			>
		</li>
		<li>
			<strong>Rate limit:</strong>
			{rateLimitText(tenant.rate_limit)}
		</li>
		<li>
			<strong>Callback endpoints:</strong>
			{#if endpointCount === null}
				see the list below
			{:else if endpointCount === 0}
				none yet. Add them below if this tenant receives asynchronous results.
			{:else}
				{endpointCount} configured, below.
			{/if}
		</li>
	</ol>
	<RoleGate {access} op="updateTenant">
		<button type="button" class="primary" onclick={onstart}>Complete setup</button>
		{#snippet denied(role)}
			<p class="hint" role="note">Only a user with the {role} role can complete setup.</p>
		{/snippet}
	</RoleGate>
</section>

<style>
	.setup {
		display: grid;
		gap: var(--space-2);
		max-width: 72rem;
		margin: var(--space-3) 0;
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--status-warn);
		border-left-width: 4px;
		border-radius: var(--radius-md);
		background: var(--status-warn-wash);
	}
	h2 {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		margin: 0;
		font-size: var(--text-md);
	}
	.lede {
		margin: 0;
		font-size: var(--text-sm);
	}
	ol {
		margin: 0;
		padding-left: var(--space-5);
		font-size: var(--text-sm);
	}
	.hint {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.primary {
		justify-self: start;
		min-height: 2.125rem;
		padding: 0 1rem;
		border: 1px solid var(--signal);
		border-radius: var(--radius-sm);
		background: var(--signal);
		color: var(--surface);
		font-weight: 700;
		cursor: pointer;
	}
</style>

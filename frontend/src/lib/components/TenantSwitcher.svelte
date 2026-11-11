<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { tenantLabel, type SwitchTarget } from '$lib/auth/tenant-choice';

	/**
	 * Tenant switcher (HUB-494): the tenants ISFC lists for this panel, the platform tenant first.
	 * The choice is kept in the session and sent as `X-Tenant-Id` on every admin call. With
	 * JavaScript, picking a tenant posts it in the background and then reloads every page load
	 * (caches included) and `/me`, so the roles of the new tenant apply at once; without it, the
	 * form posts and the page reloads. The server checks the tenant against ISFC either way.
	 */
	let {
		choices,
		currentId,
		error = null,
		next
	}: {
		choices: SwitchTarget[];
		currentId: string | null;
		error?: string | null;
		/** Where to come back to after a plain form post. */
		next: string;
	} = $props();

	// Follows the current tenant; the operator's pick overrides it until the page data changes.
	let selected = $derived(currentId ?? '');
	let busy = $state(false);
	let message = $state<string | null>(null);

	async function onsubmit(e: SubmitEvent) {
		e.preventDefault();
		if (busy || !selected || selected === currentId) return;
		busy = true;
		message = null;
		try {
			const res = await fetch('/tenant', {
				method: 'POST',
				headers: { accept: 'application/json' },
				body: new URLSearchParams({ tenant_id: selected, next })
			});
			const out = (await res.json().catch(() => null)) as { ok?: boolean; message?: string } | null;
			if (!res.ok || !out?.ok) {
				message = out?.message ?? 'The tenant could not be switched. Try again.';
				selected = currentId ?? '';
			} else {
				// Load functions are the console's caches: rerun all of them, `/me` included.
				await invalidateAll();
			}
		} catch {
			message = 'The tenant could not be switched. Try again.';
			selected = currentId ?? '';
		} finally {
			busy = false;
		}
	}
</script>

{#if choices.length > 0}
	<form class="switcher" method="POST" action="/tenant" {onsubmit}>
		<input type="hidden" name="next" value={next} />
		<label for="tenant-switch">Tenant</label>
		<select
			id="tenant-switch"
			name="tenant_id"
			bind:value={selected}
			disabled={busy}
			onchange={(e) => e.currentTarget.form?.requestSubmit()}
		>
			{#each choices as c (c.id)}
				<option value={c.id}>{tenantLabel(c)}</option>
			{/each}
		</select>
		<noscript><button type="submit">Switch</button></noscript>
		{#if message}<span class="problem" role="alert">{message}</span>{/if}
	</form>
{:else if error}
	<p class="problem" role="status">{error}</p>
{/if}

<style>
	.switcher {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		margin: 0;
	}
	label {
		color: var(--ink-muted);
		font-size: var(--text-xs);
		font-weight: 600;
	}
	select {
		max-width: 14rem;
		height: 2rem;
		padding: 0 0.4rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		color: var(--ink);
		font-size: var(--text-sm);
	}
	.problem {
		margin: 0;
		color: var(--status-danger);
		font-size: var(--text-xs);
		font-weight: 600;
	}
	@media (max-width: 640px) {
		label {
			position: absolute;
			width: 1px;
			height: 1px;
			overflow: hidden;
			clip-path: inset(50%);
		}
		select {
			max-width: 8rem;
		}
	}
</style>

<script lang="ts">
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { ApiResult } from '$lib/api/client';
	import { adminClient } from '$lib/api/client';
	import { adminApiBase } from '$lib/api/console';
	import type { Breaker, BreakerDetail } from '$lib/api/types';
	import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import { formatNumber } from '$lib/utils/format';
	import { DEFAULT_DURATION_MS, DURATIONS, type ForceKind } from './force';

	/**
	 * HUB-262: force a breaker open or closed, or clear the force (admin only). The dialog loads the
	 * breaker detail to name the integrations that use it, so the operator sees the blast radius
	 * before confirming. Force open and close also need the account id typed (F-19 rule 6).
	 */
	let {
		open = $bindable(false),
		kind,
		breaker,
		onsuccess
	}: {
		open?: boolean;
		kind: ForceKind;
		breaker: Breaker;
		onsuccess: (d: ActionSuccessData) => void;
	} = $props();

	const id = $props.id();
	const name = $derived(`${breaker.account_id} ${breaker.endpoint_ref}`);
	let duration = $state(String(DEFAULT_DURATION_MS));
	let detail = $state.raw<Promise<ApiResult<BreakerDetail>> | null>(null);

	// Fresh detail (affected integrations) each time the dialog opens.
	$effect.pre(() => {
		if (!open) return;
		duration = String(DEFAULT_DURATION_MS);
		const path = { account_id: breaker.account_id, endpoint_ref: breaker.endpoint_ref };
		detail = adminClient({ baseUrl: adminApiBase() }).call((c) =>
			c.GET('/breakers/{account_id}/{endpoint_ref}', { params: { path } })
		);
	});

	const copy = $derived(
		kind === 'open'
			? {
					title: `Force ${name} open`,
					verb: 'Force open',
					busy: 'Forcing open...',
					action: '?/forceOpen'
				}
			: kind === 'close'
				? {
						title: `Force ${name} closed`,
						verb: 'Force closed',
						busy: 'Forcing closed...',
						action: '?/forceClose'
					}
				: {
						title: `Clear the force on ${name}`,
						verb: 'Clear force',
						busy: 'Clearing...',
						action: '?/clearForce'
					}
	);
</script>

<ConfirmDialog
	bind:open
	title={copy.title}
	verb={copy.verb}
	busyVerb={copy.busy}
	action={copy.action}
	tone={kind === 'clear' ? 'primary' : 'danger'}
	subject={name}
	confirmText={kind === 'clear' ? undefined : breaker.account_id}
	{onsuccess}
>
	{#if kind === 'open'}
		<p>
			Holds the breaker open on every node within about 1 s. Calls through it follow each
			integration's circuit-open policy (fail fast, fail over or queue); async requests park until
			it closes.
		</p>
	{:else if kind === 'close'}
		<p>
			Closes the breaker and starts the ramped drain of {formatNumber(breaker.parked_count)} parked requests.
			While held closed, failures are still counted but do not trip it.
		</p>
	{:else}
		<p>
			Returns the breaker to automatic control (now forced {breaker.forced}). It re-evaluates its
			state from live traffic and probes.
		</p>
	{/if}
	<div class="affected" aria-live="polite">
		{#await detail}
			<p class="muted">Loading the integrations that use this breaker...</p>
		{:then d}
			{#if d?.ok}
				{@const list = d.data.integrations}
				{#if list.length}
					<p>
						<strong
							>Affects {list.length}
							{list.length === 1 ? 'integration' : 'integrations'}:</strong
						>
						{#each list as i, n (i)}<code>{i}</code>{n < list.length - 1 ? ', ' : ''}{/each}
					</p>
				{:else}
					<p><strong>No integration uses this breaker right now.</strong></p>
				{/if}
			{:else if d}
				<p class="warn">
					<Icon name="alert" size={14} />Could not load the affected integrations (<code
						>{d.error.code}</code
					>{d.error.requestId ? `, request ${d.error.requestId}` : ''}). Check them before you
					confirm.
				</p>
			{/if}
		{/await}
	</div>
	{#snippet fields(errors)}
		<input type="hidden" name="account_id" value={breaker.account_id} />
		<input type="hidden" name="endpoint_ref" value={breaker.endpoint_ref} />
		{#if kind !== 'clear'}
			<div class="field">
				<label for="{id}-duration">Hold for</label>
				<select id="{id}-duration" name="duration_ms" bind:value={duration}>
					{#each DURATIONS as d (d.label)}
						<option value={d.ms === null ? '' : String(d.ms)}>{d.label}</option>
					{/each}
				</select>
				{#if errors.duration_ms}<p class="field-error">{errors.duration_ms}</p>{/if}
			</div>
		{/if}
	{/snippet}
</ConfirmDialog>

<style>
	.affected p {
		margin: 0;
	}
	.warn {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.3rem;
		color: var(--status-warn);
		font-weight: 600;
	}
	.field {
		display: grid;
		gap: 0.25rem;
	}
	label {
		font-weight: 600;
		font-size: var(--text-sm);
	}
	select {
		height: 2.125rem;
		padding: 0 0.5rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-sm);
		max-width: 16rem;
	}
	.field-error {
		margin: 0;
		color: var(--status-danger);
		font-size: var(--text-sm);
		font-weight: 600;
	}
</style>

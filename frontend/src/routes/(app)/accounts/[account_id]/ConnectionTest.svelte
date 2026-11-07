<script lang="ts">
	import type { SubmitFunction } from '@sveltejs/kit';
	import { enhance } from '$app/forms';
	import { fromPlainError, type ActionFailureData, type ActionSuccessData } from '$lib/api/actions';
	import { HubApiError } from '$lib/api/errors';
	import type { ConnectionTestResult } from '$lib/api/types';
	import ErrorPanel from '$lib/components/ErrorPanel.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { formatDurationMs, formatUtc } from '$lib/utils/format';

	/**
	 * "Test connection" (HUB-291, `testAccountConnection`, editor): runs the vendor's health probe
	 * (dry where supported) with the active or the next credential. Non-mutating, no reason; the
	 * Hub audits it. The outcome is 200 either way (`ok`); the call itself failing shows code and
	 * request id. `ontested` reports each outcome (the rotation stepper uses it).
	 */
	let {
		accountId,
		slots,
		endpoints,
		defaultSlot = 'active',
		ontested
	}: {
		accountId: string;
		/** The slots that exist and can be tested. */
		slots: ('active' | 'next')[];
		endpoints: string[];
		defaultSlot?: 'active' | 'next';
		ontested?: (result: ConnectionTestResult) => void;
	} = $props();

	const uid = $props.id();
	// Starts at the default slot (next while a rotation is waiting); the operator may switch.
	let slot = $derived<'active' | 'next'>(
		slots.includes(defaultSlot) ? defaultSlot : (slots[0] ?? 'active')
	);
	let endpoint = $state('');
	let running = $state(false);
	let result = $state.raw<ConnectionTestResult | null>(null);
	let error = $state.raw<HubApiError | null>(null);

	const submit: SubmitFunction = ({ cancel }) => {
		if (running) return cancel();
		running = true;
		error = null;
		return async ({ result: r }) => {
			try {
				if (r.type === 'success') {
					result = (r.data as ActionSuccessData<ConnectionTestResult>).result;
					ontested?.(result);
				} else if (r.type === 'failure') {
					const d = r.data as ActionFailureData | undefined;
					result = null;
					error = d?.error
						? fromPlainError(d.error)
						: new HubApiError({
								status: r.status,
								code: 'VALIDATION_FAILED',
								message: Object.values(d?.fields ?? {})[0] ?? 'The test could not run.'
							});
				} else {
					error = new HubApiError({
						status: r.type === 'error' ? (r.status ?? 500) : 500,
						code: 'UNEXPECTED_RESPONSE',
						message: 'The console could not run the test. Try again.'
					});
				}
			} finally {
				running = false;
			}
		};
	};
</script>

<form
	method="POST"
	action="?/testConnection"
	use:enhance={submit}
	class="test"
	aria-label="Test connection of {accountId}"
>
	<div class="fk-field">
		<label for="{uid}-slot">Credential</label>
		<select id="{uid}-slot" name="slot" bind:value={slot}>
			{#each slots as s (s)}<option value={s}
					>{s === 'active' ? 'Active' : 'Next (not promoted)'}</option
				>{/each}
		</select>
	</div>
	{#if endpoints.length}
		<div class="fk-field">
			<label for="{uid}-ep">Endpoint</label>
			<select id="{uid}-ep" name="endpoint_ref" bind:value={endpoint}>
				<option value="">The vendor's health probe</option>
				{#each endpoints as e (e)}<option value={e}>{e}</option>{/each}
			</select>
		</div>
	{/if}
	<button type="submit" class="btn" aria-disabled={running ? 'true' : undefined}>
		<Icon name="health" size={16} />{running ? 'Testing...' : 'Test connection'}
	</button>
</form>

{#if result}
	<div class="outcome" class:failed={!result.ok} role="status">
		<StatusBadge
			tone={result.ok ? 'ok' : 'danger'}
			icon={result.ok ? 'closed' : 'open'}
			label={result.ok ? 'Connection works' : 'Connection failed'}
		/>
		<p>
			{result.slot === 'next' ? 'Next' : 'Active'} credential v{result.cred_version}{result.endpoint_ref
				? ` on ${result.endpoint_ref}`
				: ''}: {result.upstream_status == null
				? 'no answer from the vendor'
				: `vendor answered HTTP ${result.upstream_status}`}{result.latency_ms == null
				? ''
				: ` in ${formatDurationMs(result.latency_ms)}`}. Checked {formatUtc(result.checked_at)}.
		</p>
		{#if result.error_code || result.message}
			<p>
				{#if result.error_code}<code>{result.error_code}</code>{/if}
				{result.message ?? ''}
			</p>
		{/if}
	</div>
{/if}
{#if error}
	<ErrorPanel {error} context="Could not test the connection" headingLevel={3} />
{/if}

<style>
	.test {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: var(--space-3);
	}
	.outcome {
		display: grid;
		gap: var(--space-1);
		justify-items: start;
		margin-top: var(--space-2);
		padding: var(--space-2) var(--space-3);
		border-left: 4px solid var(--status-ok);
		background: var(--status-ok-wash);
		font-size: var(--text-sm);
	}
	.outcome.failed {
		border-left-color: var(--status-danger);
		background: var(--status-danger-wash);
	}
	.outcome p {
		margin: 0;
	}
</style>

<script lang="ts">
	import type { Attempt } from '$lib/api/types';
	import { breakerStatus } from '$lib/status';
	import { formatDurationMs, formatUtc } from '$lib/utils/format';
	import StatusBadge from './StatusBadge.svelte';

	/**
	 * Upstream attempts in `attempt_no` order (request timeline HUB-264, DLQ entry HUB-266): the
	 * integration version each used, account and endpoint, timing, vendor status, error, breaker
	 * state and credential version. Tenant viewers (`scoped`) see no account or credential (F-19
	 * rule 5).
	 */
	let {
		attempts,
		scoped,
		label = 'Attempts',
		formatTime = formatUtc
	}: {
		attempts: Attempt[];
		scoped: boolean;
		/** Accessible name of the scroll region. */
		label?: string;
		/** How the start time is written (the timeline uses millisecond clock times). */
		formatTime?: (iso: string) => string;
	} = $props();

	const sorted = $derived([...attempts].sort((x, y) => x.attempt_no - y.attempt_no));
</script>

<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<div class="table-scroll" role="region" aria-label={label} tabindex="0">
	<table>
		<caption class="visually-hidden"
			>Upstream attempts with the integration version each used</caption
		>
		<thead>
			<tr>
				<th scope="col" class="numeric">Attempt</th>
				<th scope="col">Integration version</th>
				{#if !scoped}<th scope="col">Account</th>{/if}
				<th scope="col">Endpoint</th>
				<th scope="col">Served by</th>
				<th scope="col">Started</th>
				<th scope="col" class="numeric">Duration</th>
				<th scope="col" class="numeric">Vendor status</th>
				<th scope="col">Error</th>
				<th scope="col">Breaker</th>
				{#if !scoped}<th scope="col" class="numeric">Credential</th>{/if}
			</tr>
		</thead>
		<tbody>
			{#each sorted as at (at.attempt_no)}
				<tr>
					<th scope="row" class="numeric">{at.attempt_no}</th>
					<td>v{at.integration_version}</td>
					{#if !scoped}<td><code>{at.vendor_id}/{at.account_id}</code></td>{/if}
					<td><code>{at.endpoint_ref}</code></td>
					<td>{at.served_by ?? '-'}</td>
					<td><time datetime={at.started_at}>{formatTime(at.started_at)}</time></td>
					<td class="numeric">{formatDurationMs(at.duration_ms)}</td>
					<td class="numeric">{at.vendor_status ?? '-'}</td>
					<td
						>{#if at.error_code}<code>{at.error_code}</code>{:else}-{/if}</td
					>
					<td>
						{#if at.breaker_state}
							{@const b = breakerStatus[at.breaker_state]}
							<StatusBadge tone={b.tone} icon={b.icon} label={b.label} />
						{:else}-{/if}
					</td>
					{#if !scoped}<td class="numeric">{at.cred_version ?? '-'}</td>{/if}
				</tr>
			{/each}
		</tbody>
	</table>
</div>

<style>
	.table-scroll {
		overflow-x: auto;
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: var(--text-sm);
		font-variant-numeric: tabular-nums;
	}
	th,
	td {
		height: var(--row-height);
		padding: 0 var(--space-3);
		text-align: left;
		white-space: nowrap;
		border-bottom: 1px solid var(--rule);
	}
	thead th {
		background: var(--surface-sunken);
		color: var(--ink-muted);
		font-size: var(--text-xs);
		font-weight: 600;
	}
	.numeric {
		text-align: right;
	}
</style>

<script lang="ts">
	import type { IntegrationSpec } from '$lib/api/types';
	import { formatDurationMs, formatRatio } from '$lib/utils/format';

	/**
	 * Read-only view of one integration version's spec (HUB-257): targets and weights, failover,
	 * delivery, breaker override, webhooks, audit and shadow. Shows only what the API returned
	 * (a tenant viewer may get less); never secrets (specs hold none).
	 */
	let { spec, headingLevel = 3 }: { spec: IntegrationSpec; headingLevel?: 3 | 4 } = $props();

	const h = $derived(`h${headingLevel}`);
	const uid = $props.id();
	const totalWeight = $derived(spec.targets.reduce((n, t) => n + t.weight, 0));

	const onCircuitOpen: Record<string, string> = {
		fail_fast: 'Fail fast (503 with retry-after)',
		failover: 'Fail over to the next target',
		queue_if_allowed: 'Queue if the mode allows it'
	};
	const pending: Record<string, string> = {
		re_resolve: 'Re-resolve at send time',
		pinned: 'Pinned to the version at accept time'
	};
	const payloads: Record<string, string> = {
		full: 'Full payloads',
		masked: 'Masked payloads',
		sealed: 'Sealed (unseal is audited)',
		metadata_only: 'Metadata only (size and SHA-256)'
	};
	const json = (v: unknown) => JSON.stringify(v);
</script>

<div class="spec">
	<section aria-labelledby="{uid}-targets">
		<svelte:element this={h} id="{uid}-targets">Targets</svelte:element>
		<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
		<div class="table-wrap" role="region" aria-label="Targets table" tabindex="0">
			<table>
				<thead>
					<tr>
						<th scope="col">Target (vendor/account)</th>
						<th scope="col">Request transformer</th>
						<th scope="col">Response transformer</th>
						<th scope="col" class="numeric">Weight</th>
						<th scope="col">Params</th>
					</tr>
				</thead>
				<tbody>
					{#each spec.targets as t, i (i)}
						<tr>
							<th scope="row"><code>{t.vendor_id}/{t.account_id}</code></th>
							<td><code>{t.transformer.request}</code></td>
							<td><code>{t.transformer.response}</code></td>
							<td class="numeric">{t.weight}%</td>
							<td
								>{#if t.params}<code>{json(t.params)}</code>{:else}<span class="muted">None</span
									>{/if}</td
							>
						</tr>
					{/each}
				</tbody>
				<tfoot>
					<tr>
						<td colspan="3">Total</td>
						<td class="numeric">{totalWeight}%</td>
						<td></td>
					</tr>
				</tfoot>
			</table>
		</div>
	</section>

	<section aria-labelledby="{uid}-failover">
		<svelte:element this={h} id="{uid}-failover">Failover</svelte:element>
		{#if spec.failover?.length}
			<p class="note">Tried in this order when the target's breaker is open.</p>
			<ol class="failover">
				{#each spec.failover as f, i (i)}
					<li>
						<code>{f.vendor_id}/{f.account_id}</code>
						<span class="muted"
							>request <code>{f.transformer.request}</code>, response
							<code>{f.transformer.response}</code></span
						>
					</li>
				{/each}
			</ol>
		{:else}
			<p class="note">No failover targets.</p>
		{/if}
	</section>

	<section aria-labelledby="{uid}-delivery">
		<svelte:element this={h} id="{uid}-delivery">Delivery</svelte:element>
		<dl class="facts">
			<div>
				<dt>Mode</dt>
				<dd><code>{spec.delivery.mode}</code></dd>
			</div>
			<div>
				<dt>Wait window</dt>
				<dd>{formatDurationMs(spec.delivery.wait_ms)}</dd>
			</div>
			<div>
				<dt>Time to live</dt>
				<dd>{formatDurationMs(spec.delivery.ttl_ms)}</dd>
			</div>
			<div>
				<dt>Circuit open</dt>
				<dd>{onCircuitOpen[spec.delivery.on_circuit_open ?? 'fail_fast']}</dd>
			</div>
			<div>
				<dt>Pending at cutover</dt>
				<dd>{pending[spec.delivery.pending ?? 're_resolve']}</dd>
			</div>
			<div>
				<dt>Retry schedule</dt>
				<dd>
					{#if spec.delivery.retry?.schedule_ms?.length}
						{spec.delivery.retry.schedule_ms.map(formatDurationMs).join(', ')}
					{:else}<span class="muted">Default</span>{/if}
				</dd>
			</div>
			<div>
				<dt>Max attempts</dt>
				<dd>{spec.delivery.retry?.max_attempts ?? 'Default'}</dd>
			</div>
		</dl>
	</section>

	<section aria-labelledby="{uid}-breaker">
		<svelte:element this={h} id="{uid}-breaker">Breaker</svelte:element>
		{#if spec.breaker?.override && Object.keys(spec.breaker.override).length}
			{@const o = spec.breaker.override}
			<dl class="facts">
				{#if o.open_after_consecutive_timeouts != null}
					<div>
						<dt>Open after consecutive timeouts</dt>
						<dd>{o.open_after_consecutive_timeouts}</dd>
					</div>
				{/if}
				{#if o.failure_ratio != null}
					<div>
						<dt>Failure ratio</dt>
						<dd>{formatRatio(o.failure_ratio)}</dd>
					</div>
				{/if}
				{#if o.min_calls != null}
					<div>
						<dt>Minimum calls</dt>
						<dd>{o.min_calls}</dd>
					</div>
				{/if}
			</dl>
		{:else}
			<p class="note">No override: the vendor's breaker defaults apply.</p>
		{/if}
	</section>

	<section aria-labelledby="{uid}-webhooks">
		<svelte:element this={h} id="{uid}-webhooks">Webhooks</svelte:element>
		{#if spec.webhooks?.length}
			<ul class="plain">
				{#each spec.webhooks as w, i (i)}
					<li><code>{w.event}</code> to <code>{w.endpoint_id}</code></li>
				{/each}
			</ul>
		{:else}
			<p class="note">No webhook subscriptions.</p>
		{/if}
	</section>

	<section aria-labelledby="{uid}-audit">
		<svelte:element this={h} id="{uid}-audit">Audit</svelte:element>
		<dl class="facts">
			<div>
				<dt>Payloads</dt>
				<dd>{payloads[spec.audit.payloads] ?? spec.audit.payloads}</dd>
			</div>
			<div>
				<dt>Retention</dt>
				<dd>{formatDurationMs(spec.audit.retention_ms)}</dd>
			</div>
		</dl>
	</section>

	<section aria-labelledby="{uid}-shadow">
		<svelte:element this={h} id="{uid}-shadow">Shadow traffic</svelte:element>
		{#if spec.shadow}
			<dl class="facts">
				<div>
					<dt>Target</dt>
					<dd><code>{spec.shadow.target.vendor_id}/{spec.shadow.target.account_id}</code></dd>
				</div>
				<div>
					<dt>Mode</dt>
					<dd>
						{spec.shadow.mode === 'send' ? 'Send (result logged)' : 'Dry run (transform only)'}
					</dd>
				</div>
				<div>
					<dt>Sample</dt>
					<dd>{formatRatio(spec.shadow.sample_ratio ?? 1)}</dd>
				</div>
			</dl>
		{:else}
			<p class="note">Off.</p>
		{/if}
	</section>
</div>

<style>
	.spec {
		display: grid;
		gap: var(--space-5);
	}
	/* Grid items default to their content width; let the targets table scroll inside its wrapper
	   instead of widening the page on a phone (HUB-287). */
	.spec > section {
		min-width: 0;
	}
	section > :global(:is(h3, h4)) {
		margin-bottom: var(--space-2);
		font-size: var(--text-md);
	}
	.note {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.table-wrap {
		overflow-x: auto;
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: var(--text-sm);
		font-variant-numeric: tabular-nums;
		background: var(--surface);
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
	tbody th {
		font-weight: 400;
	}
	tfoot td {
		border-bottom: 0;
		color: var(--ink-muted);
		font-weight: 600;
	}
	.numeric {
		text-align: right;
	}
	.failover {
		margin: var(--space-2) 0 0;
		padding-left: var(--space-5);
		font-size: var(--text-sm);
	}
	.failover li + li {
		margin-top: var(--space-1);
	}
	.plain {
		margin: 0;
		padding-left: var(--space-5);
		font-size: var(--text-sm);
	}
</style>

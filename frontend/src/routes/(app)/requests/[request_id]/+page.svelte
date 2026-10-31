<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import AttemptsTable from '$lib/components/AttemptsTable.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Payload from '$lib/components/Payload.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { requestStatus } from '$lib/status';
	import { formatBytes, formatDurationMs, formatNumber, formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';
	import { clock, eventLabels, gap, payloadView, timed } from './timeline';

	let { data }: { data: PageData } = $props();

	const id = $derived(data.requestId);
	const scoped = $derived(data.access.tenantScoped);
	const policyLabels: Record<string, string> = {
		full: 'Full payloads',
		masked: 'Masked payloads (PII hashed)',
		sealed: 'Sealed payloads (encrypted, unseal is audited)',
		metadata_only: 'Metadata only (no bodies)'
	};
	/** Result and error objects follow the same policy as the events. */
	const objectView = (
		v: Record<string, unknown> | null | undefined,
		sealed: boolean,
		policy: string
	) =>
		sealed
			? ({ kind: 'sealed' } as const)
			: v
				? ({ kind: 'shown', value: v } as const)
				: policy === 'metadata_only'
					? ({ kind: 'metadata_only' } as const)
					: ({ kind: 'none' } as const);
</script>

<PageHeader
	title={id}
	summary="What happened to this request, event by event, with its attempts."
/>

<AsyncView result={data.request} noun="request {id}" onretry={invalidateAll}>
	{#snippet children(r)}
		{@const s = requestStatus[r.status]}
		<section class="head" aria-label="Request summary">
			<div class="status-line">
				<StatusBadge tone={s.tone} icon={s.icon} label={s.label} />
				{#if r.code}<code>{r.code}</code>{/if}
				<span class="rid"><code class="id">{r.request_id}</code></span>
				<CopyButton value={r.request_id} label="Copy request id" />
			</div>
			<dl class="facts">
				<div>
					<dt>Tenant</dt>
					<dd>{r.tenant_id}</dd>
				</div>
				<div>
					<dt>Integration</dt>
					<dd>
						<a
							href={resolve('/(app)/integrations/[integration_id]', {
								integration_id: r.integration_id
							})}><code>{r.integration_id}</code></a
						>{r.final_version != null ? ` v${r.final_version}` : ''}
					</dd>
				</div>
				<div>
					<dt>Capability</dt>
					<dd><code>{r.capability}</code> {r.operation}</dd>
				</div>
				<div>
					<dt>Mode</dt>
					<dd>{r.mode}</dd>
				</div>
				{#if !scoped}
					<div>
						<dt>Vendor / account (last attempt)</dt>
						<dd>{r.vendor_id ?? '-'}{r.account_id ? ` / ${r.account_id}` : ''}</dd>
					</div>
				{/if}
				<div>
					<dt>Attempts</dt>
					<dd>{formatNumber(r.attempts)}</dd>
				</div>
				<div>
					<dt>Created</dt>
					<dd>{formatUtc(r.created_at)}</dd>
				</div>
				<div>
					<dt>Completed</dt>
					<dd>{formatUtc(r.completed_at)}</dd>
				</div>
				<div>
					<dt>Total</dt>
					<dd>{formatDurationMs(r.timings?.total_ms ?? r.duration_ms)}</dd>
				</div>
				<div>
					<dt>Hub overhead</dt>
					<dd>{formatDurationMs(r.timings?.hub_overhead_ms ?? r.hub_overhead_ms)}</dd>
				</div>
				<div>
					<dt>Vendor</dt>
					<dd>{formatDurationMs(r.timings?.vendor_ms)}</dd>
				</div>
				<div>
					<dt>Queue wait</dt>
					<dd>{formatDurationMs(r.timings?.queue_wait_ms)}</dd>
				</div>
				<div>
					<dt>Idempotency key</dt>
					<dd>
						{#if r.idempotency_key}<code>{r.idempotency_key}</code>{:else}None{/if}
					</dd>
				</div>
				{#if r.next_attempt_at}
					<div>
						<dt>Next attempt</dt>
						<dd>{formatUtc(r.next_attempt_at)}</dd>
					</div>
				{/if}
				{#if r.expires_at}
					<div>
						<dt>Expires</dt>
						<dd>{formatUtc(r.expires_at)}</dd>
					</div>
				{/if}
				<div>
					<dt>Audit policy</dt>
					<dd>{policyLabels[r.payload_policy] ?? r.payload_policy}</dd>
				</div>
				<div>
					<dt>Snapshot</dt>
					<dd>{r.snapshot_version ?? '-'}</dd>
				</div>
				{#if r.trace_id}
					<div>
						<dt>Trace id</dt>
						<dd class="copy">
							<code>{r.trace_id}</code><CopyButton value={r.trace_id} label="Copy trace id" />
						</dd>
					</div>
				{/if}
			</dl>
			<div class="outcome">
				<h2>Result</h2>
				<Payload view={objectView(r.result, r.sealed, r.payload_policy)} label="result" />
				{#if r.error}
					<h2>Error</h2>
					<Payload view={objectView(r.error, r.sealed, r.payload_policy)} label="error" />
				{/if}
			</div>
		</section>
	{/snippet}
</AsyncView>

<section class="block" aria-labelledby="timeline-heading">
	<h2 id="timeline-heading">Timeline</h2>
	<AsyncView result={data.timeline} noun="the timeline" onretry={invalidateAll} headingLevel={3}>
		{#snippet children(t)}
			{@const events = timed(t.events)}
			<p class="meta" role="status">
				{events.length}
				{events.length === 1 ? 'event' : 'events'} in sequence order. Sizes and SHA-256 hashes are kept
				for every payload, whatever the policy.
			</p>
			<ol class="timeline">
				{#each events as { event: e, sincePrevious, sinceStart } (e.seq)}
					<li class:sealed={e.sealed}>
						<div class="when">
							<span class="seq" aria-label="Sequence {e.seq}">#{e.seq}</span>
							<time datetime={e.at}>{clock(e.at)}</time>
							<span class="gap">{gap(sincePrevious)}</span>
						</div>
						<div class="what">
							<p class="type">
								<strong>{eventLabels[e.type] ?? e.type}</strong>
								<code class="raw">{e.type}</code>
								{#if e.attempt_no}<span class="tag">attempt {e.attempt_no}</span>{/if}
								<span class="since">at {formatNumber(sinceStart)} ms</span>
							</p>
							<p class="size">
								{formatBytes(e.bytes)}{#if e.truncated}, truncated at 64 KB{e.payload_ref
										? `; the rest is in object storage (${e.payload_ref})`
										: ''}{/if}
							</p>
							<Payload view={payloadView(e, t.payload_policy)} bytes={e.bytes} sha256={e.sha256} />
						</div>
					</li>
				{:else}
					<li>No events recorded.</li>
				{/each}
			</ol>
		{/snippet}
	</AsyncView>
</section>

<section class="block" aria-labelledby="attempts-heading">
	<h2 id="attempts-heading">Attempts</h2>
	<AsyncView result={data.attempts} noun="the attempts" onretry={invalidateAll} headingLevel={3}>
		{#snippet children(a)}
			{#if a.items.length === 0}
				<p class="meta">No upstream attempt was made.</p>
			{:else}
				<AttemptsTable attempts={a.items} {scoped} formatTime={clock} />
			{/if}
		{/snippet}
	</AsyncView>
</section>

<style>
	.head {
		display: grid;
		gap: var(--space-3);
		max-width: 72rem;
		padding-bottom: var(--space-4);
		border-bottom: 1px solid var(--rule);
	}
	.status-line,
	.copy {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}
	.rid {
		color: var(--ink-muted);
	}
	.outcome {
		display: grid;
		gap: var(--space-2);
	}
	.outcome h2 {
		font-size: var(--text-md);
	}
	.block {
		margin-top: var(--space-5);
		max-width: 72rem;
	}
	.block > h2 {
		font-size: var(--text-lg);
		margin-bottom: var(--space-2);
	}
	.meta {
		margin: 0 0 var(--space-3);
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.timeline {
		display: grid;
		margin: 0;
		padding: 0;
		list-style: none;
		border-left: 2px solid var(--rule-strong);
	}
	.timeline li {
		position: relative;
		display: grid;
		grid-template-columns: 12rem 1fr;
		gap: var(--space-3);
		padding: var(--space-2) 0 var(--space-3) var(--space-4);
	}
	.timeline li::before {
		content: '';
		position: absolute;
		left: -0.4rem;
		top: 0.85rem;
		width: 0.7rem;
		height: 0.7rem;
		border: 2px solid var(--signal);
		border-radius: 50%;
		background: var(--surface);
	}
	.timeline li.sealed::before {
		border-color: var(--ink-muted);
		border-style: dashed;
	}
	.when {
		display: grid;
		align-content: start;
		font-size: var(--text-sm);
		font-variant-numeric: tabular-nums;
	}
	.seq {
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.gap {
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.what {
		display: grid;
		gap: 0.3rem;
		min-width: 0;
	}
	.type {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: var(--space-2);
		margin: 0;
	}
	.raw,
	.since,
	.size {
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.size {
		margin: 0;
	}
	.tag {
		padding: 0 0.35rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		font-size: var(--text-xs);
	}
	@media (max-width: 900px) {
		.timeline li {
			grid-template-columns: 1fr;
		}
	}
</style>

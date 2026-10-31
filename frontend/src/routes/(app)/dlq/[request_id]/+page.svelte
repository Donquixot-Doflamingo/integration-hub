<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import AttemptsTable from '$lib/components/AttemptsTable.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Payload from '$lib/components/Payload.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { formatNumber, formatUtc } from '$lib/utils/format';
	import { shownOrNone } from '$lib/utils/payload';
	import type { PageData } from './$types';

	/**
	 * DLQ entry (HUB-266, DESIGN §10.6): why it was dead-lettered (reason + message), the attempt
	 * history, the canonical payload and the last vendor request and response. Payloads are shown as
	 * the API returned them (shaped by the payload policy, secrets as `***`), with secret-looking
	 * fields hidden as a second line of defence. Tenant viewers see no vendor or account (F-19 rule 5),
	 * so the vendor request and response (vendor URL, headers) are not shown to them.
	 */
	let { data }: { data: PageData } = $props();

	const id = $derived(data.requestId);
	const scoped = $derived(data.access.tenantScoped);
</script>

<PageHeader
	title={id}
	summary="Why this request was dead-lettered, what was sent and what the vendor answered."
/>

<AsyncView result={data.entry} noun="dead letter {id}" onretry={invalidateAll}>
	{#snippet children(d)}
		<section class="head" aria-label="Dead letter summary">
			<div class="status-line">
				{#if d.replayed_at}
					<StatusBadge tone="ok" icon="retry" label={`Replayed ${formatUtc(d.replayed_at)}`} />
				{:else}
					<StatusBadge tone="warn" icon="dlq" label="Waiting for a replay" />
				{/if}
				<span class="rid"><code class="id">{d.request_id}</code></span>
				<CopyButton value={d.request_id} label="Copy request id" />
				<a href={resolve('/(app)/requests/[request_id]', { request_id: d.request_id })}
					>Open the request timeline</a
				>
			</div>

			<div class="reason" role="group" aria-labelledby="reason-heading">
				<h2 id="reason-heading">Reason</h2>
				<p>
					<code class="code">{d.reason}</code>{#if d.reason === 'poison'}<span class="muted">
							(the envelope crashed a worker twice)</span
						>{/if}
				</p>
				<p class="message">{d.message ?? 'No message was recorded.'}</p>
			</div>

			<dl class="facts">
				<div>
					<dt>Tenant</dt>
					<dd>{d.tenant_id}</dd>
				</div>
				<div>
					<dt>Integration</dt>
					<dd>
						<a
							href={resolve('/(app)/integrations/[integration_id]', {
								integration_id: d.integration_id
							})}><code>{d.integration_id}</code></a
						>
					</dd>
				</div>
				{#if !scoped}
					<div>
						<dt>Vendor / account (last attempt)</dt>
						<dd>{d.vendor_id ?? '-'}{d.account_id ? ` / ${d.account_id}` : ''}</dd>
					</div>
				{/if}
				<div>
					<dt>Attempts</dt>
					<dd>{formatNumber(d.attempts)}</dd>
				</div>
				<div>
					<dt>Dead-lettered</dt>
					<dd>{formatUtc(d.dead_lettered_at)}</dd>
				</div>
				<div>
					<dt>Expires</dt>
					<dd>{formatUtc(d.expires_at)} <span class="muted">(14-day retention)</span></dd>
				</div>
			</dl>
		</section>

		<section class="block" aria-labelledby="history-heading">
			<h2 id="history-heading">Attempt history</h2>
			{#if d.attempt_history.length === 0}
				<p class="meta">No upstream attempt was made (it failed before reaching a vendor).</p>
			{:else}
				<AttemptsTable attempts={d.attempt_history} {scoped} label="Attempts table" />
			{/if}
		</section>

		<section class="block payloads" aria-labelledby="payloads-heading">
			<h2 id="payloads-heading">Payloads</h2>
			<p class="meta">
				As the audit policy stored them: masked values stay masked and credentials show as
				<code>***</code>.
			</p>
			<div class="payload">
				<h3>Canonical request</h3>
				<Payload
					view={shownOrNone(d.canonical)}
					label="canonical request"
					emptyText="No canonical request was returned (the audit policy may not keep bodies)."
				/>
			</div>
			{#if scoped}
				<p class="meta" role="note">
					The last vendor request and response name the vendor, which tenant viewers cannot see.
				</p>
			{:else}
				<div class="payload">
					<h3>Last vendor request</h3>
					<Payload
						view={shownOrNone(d.last_vendor_request)}
						label="vendor request"
						emptyText="No vendor request was returned."
					/>
				</div>
				<div class="payload">
					<h3>Last vendor response</h3>
					<Payload
						view={shownOrNone(d.last_vendor_response)}
						label="vendor response"
						emptyText="No vendor response was returned (the vendor may not have answered)."
					/>
				</div>
			{/if}
		</section>
	{/snippet}
</AsyncView>

<style>
	.head {
		display: grid;
		gap: var(--space-3);
		max-width: 72rem;
		padding-bottom: var(--space-4);
		border-bottom: 1px solid var(--rule);
	}
	.status-line {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}
	.rid {
		color: var(--ink-muted);
	}
	.reason {
		padding: var(--space-3) var(--space-4);
		border-left: 4px solid var(--status-danger);
		background: var(--status-danger-wash);
		border-radius: var(--radius-sm);
	}
	.reason h2 {
		font-size: var(--text-xs);
		color: var(--ink-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}
	.reason p {
		margin: var(--space-1) 0 0;
	}
	.code {
		font-size: var(--text-md);
		font-weight: 700;
	}
	.message {
		overflow-wrap: anywhere;
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
	.payloads {
		display: grid;
		gap: var(--space-2);
	}
	.payload h3 {
		font-size: var(--text-md);
		margin-bottom: var(--space-1);
	}
</style>

<script lang="ts">
	import type { TransformerDryRunResult } from '$lib/api/types';
	import { formatNumber } from '$lib/utils/format';
	import { redactSecrets } from '$lib/utils/redact';
	import StatusBadge from '../StatusBadge.svelte';

	/**
	 * `dryRunTransformer` output (HUB-269): for a request transformer the exact vendor request as
	 * method, URL, headers and body; for a response transformer the canonical response or error;
	 * compile errors with their line. Secrets arrive redacted (`***`) and are shown exactly so; values
	 * under secret-looking keys that are not masked are hidden too (second line of defence). Nothing
	 * here is logged or stored.
	 */
	let { result, refLabel }: { result: TransformerDryRunResult; refLabel: string } = $props();

	const uid = $props.id();
	const request = $derived(result.vendor_request ?? null);
	const headers = $derived(
		request ? Object.entries(redactSecrets(request.headers) as Record<string, string>) : []
	);
	const json = (v: unknown) => JSON.stringify(redactSecrets(v), null, 2);
</script>

<section class="result" aria-labelledby="{uid}-title">
	<h4 id="{uid}-title" class="summary">
		<StatusBadge
			tone={result.ok ? 'ok' : 'danger'}
			icon={result.ok ? 'closed' : 'open'}
			label={result.ok ? 'Transformed' : 'Failed'}
		/>
		<span>
			Dry run of {refLabel} ({result.kind}){typeof result.duration_us === 'number'
				? `, ${formatNumber(result.duration_us)} µs`
				: ''}. Nothing was sent.
		</span>
	</h4>

	{#if result.compile_errors.length}
		<div class="errors" role="alert">
			<p><strong>Compile errors</strong></p>
			<ul>
				{#each result.compile_errors as e, i (i)}
					<li>
						{#if e.line}<span class="where">line {e.line}</span>{/if}
						{#if e.path}<code>{e.path}</code>{/if}
						{e.message}
					</li>
				{/each}
			</ul>
		</div>
	{/if}

	{#if request}
		<div role="group" aria-label="Vendor request">
			<dl class="request">
				<div class="line">
					<dt>Method</dt>
					<dd><code class="method">{request.method}</code></dd>
				</div>
				<div class="line">
					<dt>URL</dt>
					<dd><code class="url">{request.url}</code></dd>
				</div>
				<div>
					<dt>Headers</dt>
					<dd>
						{#if headers.length}
							<table class="headers">
								<caption class="visually-hidden">Vendor request headers</caption>
								<thead>
									<tr><th scope="col">Name</th><th scope="col">Value</th></tr>
								</thead>
								<tbody>
									{#each headers as [name, value] (name)}
										<tr>
											<th scope="row"><code>{name}</code></th>
											<td><code>{value}</code></td>
										</tr>
									{/each}
								</tbody>
							</table>
						{:else}
							<span class="muted">None</span>
						{/if}
					</dd>
				</div>
				<div>
					<dt>Body</dt>
					<dd>
						{#if request.body === undefined || request.body === null}
							<span class="muted">No body</span>
						{:else}
							<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
							<pre tabindex="0" aria-label="Vendor request body">{json(request.body)}</pre>
						{/if}
					</dd>
				</div>
			</dl>
		</div>
	{/if}

	{#if result.canonical_response}
		<p class="part">Canonical response</p>
		<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
		<pre tabindex="0" aria-label="Canonical response">{json(result.canonical_response)}</pre>
	{/if}
	{#if result.canonical_error}
		<p class="part">Canonical error</p>
		<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
		<pre tabindex="0" aria-label="Canonical error">{json(result.canonical_error)}</pre>
	{/if}
	{#if !request && !result.canonical_response && !result.canonical_error && !result.compile_errors.length}
		<p class="muted">The transformer produced no output.</p>
	{/if}
</section>

<style>
	.result {
		display: grid;
		gap: var(--space-3);
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
	}
	.summary {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin: 0;
		font-size: var(--text-sm);
	}
	.errors {
		padding: var(--space-2) var(--space-3);
		border-left: 4px solid var(--status-danger);
		background: var(--status-danger-wash);
		font-size: var(--text-sm);
	}
	.errors p,
	.errors ul {
		margin: 0;
	}
	.errors ul {
		padding-left: var(--space-4);
	}
	.where {
		font-weight: 700;
		margin-right: var(--space-1);
	}
	.request {
		display: grid;
		gap: var(--space-3);
		margin: 0;
	}
	.request dt,
	.part {
		margin: 0 0 var(--space-1);
		color: var(--ink-muted);
		font-size: var(--text-xs);
		font-weight: 600;
	}
	.request dd {
		margin: 0;
	}
	.line {
		display: grid;
		grid-template-columns: 5rem 1fr;
		align-items: baseline;
	}
	.method {
		font-weight: 800;
	}
	.url {
		overflow-wrap: anywhere;
	}
	.headers {
		border-collapse: collapse;
		font-size: var(--text-sm);
	}
	.headers th,
	.headers td {
		padding: 0.15rem var(--space-3) 0.15rem 0;
		text-align: left;
		border-bottom: 1px solid var(--rule);
		vertical-align: top;
	}
	.headers thead th {
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.headers tbody th {
		font-weight: 600;
	}
	pre {
		max-height: 22rem;
		overflow: auto;
		margin: 0;
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--rule);
		border-radius: var(--radius-sm);
		background: var(--surface-sunken);
		font-size: var(--text-xs);
		line-height: 1.5;
	}
</style>

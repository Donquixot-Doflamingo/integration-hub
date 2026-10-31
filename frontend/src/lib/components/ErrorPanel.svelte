<script lang="ts">
	import type { HubApiError } from '$lib/api/errors';
	import CopyButton from './CopyButton.svelte';
	import Icon from './Icon.svelte';

	let {
		error,
		context,
		onretry
	}: {
		error: HubApiError;
		/** What failed, e.g. "Could not load integrations". */
		context: string;
		onretry?: () => void;
	} = $props();

	const hint = $derived.by(() => {
		switch (error.code) {
			case 'UNAUTHORIZED':
				return 'Your session has expired. Sign in again.';
			case 'FORBIDDEN':
				return 'Your role does not allow this view. Ask an admin for access.';
			case 'NETWORK_ERROR':
				return 'The admin API did not answer. Check the connection or VPN, then retry.';
			case 'UNEXPECTED_RESPONSE':
				return 'Something between the console and the admin API answered instead of the Hub.';
			default:
				return error.retryable ? 'This is usually temporary. Retry in a moment.' : null;
		}
	});
</script>

<section class="error" role="alert" aria-labelledby="error-title">
	<Icon name="alert" />
	<div class="body">
		<h2 id="error-title">{context}</h2>
		<p class="message">{error.message}</p>
		{#if hint}<p class="hint">{hint}</p>{/if}
		<dl>
			<div>
				<dt>Code</dt>
				<dd>
					<code>{error.code}</code>{#if error.status}<span class="status">HTTP {error.status}</span
						>{/if}
				</dd>
			</div>
			<div>
				<dt>Request id</dt>
				<dd>
					{#if error.requestId}
						<code class="id">{error.requestId}</code>
						<CopyButton value={error.requestId} label="Copy request id" />
					{:else}
						<span class="none">None (the request did not reach the Hub)</span>
					{/if}
				</dd>
			</div>
		</dl>
		{#if onretry}
			<button type="button" class="retry" onclick={onretry}>
				<Icon name="retry" size={16} /> Retry
			</button>
		{/if}
	</div>
</section>

<style>
	.error {
		display: flex;
		gap: var(--space-3);
		padding: var(--space-4);
		border: 1px solid var(--status-danger);
		border-left-width: 4px;
		border-radius: var(--radius-md);
		background: var(--status-danger-wash);
		color: var(--ink);
		max-width: 46rem;
	}
	.error > :global(.icon) {
		color: var(--status-danger);
		margin-top: 0.1rem;
	}
	h2 {
		font-size: var(--text-md);
	}
	.message {
		margin: var(--space-1) 0 0;
	}
	.hint {
		margin: var(--space-1) 0 0;
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	dl {
		display: grid;
		gap: var(--space-1);
		margin: var(--space-3) 0 0;
		font-size: var(--text-sm);
	}
	dl div {
		display: grid;
		grid-template-columns: 6.5rem 1fr;
		align-items: center;
	}
	dt {
		color: var(--ink-muted);
	}
	dd {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		margin: 0;
		flex-wrap: wrap;
	}
	.status,
	.none {
		color: var(--ink-muted);
	}
	.retry {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		margin-top: var(--space-3);
		padding: 0.35rem 0.85rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		cursor: pointer;
		font-weight: 600;
	}
	.retry:hover {
		border-color: var(--ink-muted);
	}
</style>

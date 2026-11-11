<script lang="ts">
	import type { HubApiError } from '$lib/api/errors';
	import { resolve } from '$app/paths';
	import { loginUrl } from '$lib/auth/redirect';
	import { formatDurationMs } from '$lib/utils/format';
	import CopyButton from './CopyButton.svelte';
	import Icon from './Icon.svelte';

	/**
	 * The one way to show a failed admin API call (HUB-255): canonical `code`, the message, a hint for
	 * the operator, whether retrying can help, field details (`VALIDATION_FAILED`) and the request id
	 * with a copy button, which operators paste into the request explorer.
	 */
	let {
		error,
		context,
		onretry,
		headingLevel = 2
	}: {
		error: HubApiError;
		/** What failed, e.g. "Could not load integrations". */
		context: string;
		onretry?: () => void;
		/** Heading level that fits where the panel sits (2 on a page, 3 inside a section). */
		headingLevel?: 2 | 3;
	} = $props();

	const id = $props.id();

	const hint = $derived.by(() => {
		switch (error.code) {
			case 'UNAUTHORIZED':
				return 'Your session has expired. Sign in again.';
			case 'FORBIDDEN':
				return 'Your role does not allow this view. Ask an admin for access.';
			case 'NOT_FOUND':
				return 'It may have been removed, or it is outside your tenants.';
			case 'RATE_LIMITED':
				return 'The admin API is rate limiting this console.';
			case 'NETWORK_ERROR':
				return 'The admin API did not answer. Check the connection or VPN, then retry.';
			case 'UNEXPECTED_RESPONSE':
				return 'Something between the console and the admin API answered instead of the Hub.';
			default:
				return null;
		}
	});

	// An expired session cannot be retried away: offer sign-in, returning to this page (HUB-455).
	const expired = $derived(error.code === 'UNAUTHORIZED');
	const signInHref = $derived(
		resolve('/login') +
			(typeof window === 'undefined'
				? ''
				: new URL(loginUrl(new URL(window.location.href), { expired: true }), 'http://x').search)
	);

	const retryText = $derived(
		error.retryable
			? error.retryAfterMs
				? `Yes, after ${formatDurationMs(error.retryAfterMs)}`
				: 'Yes, this is usually temporary'
			: 'No, retrying gives the same answer'
	);
</script>

<section class="error" role="alert" aria-labelledby="{id}-title">
	<Icon name="alert" />
	<div class="body">
		<svelte:element this={`h${headingLevel}`} id="{id}-title" class="title"
			>{context}</svelte:element
		>
		<p class="message">{error.message}</p>
		{#if hint}<p class="hint">{hint}</p>{/if}
		{#if error.details.length}
			<ul class="details" aria-label="Details">
				{#each error.details as d, i (i)}
					<li>
						{#if d.pointer}<code>{d.pointer}</code>{/if}
						{d.message}
					</li>
				{/each}
			</ul>
		{/if}
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
			<div>
				<dt>Retry helps</dt>
				<dd>{retryText}</dd>
			</div>
		</dl>
		{#if expired}
			<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- resolve('/login') plus the query -->
			<a class="retry" href={signInHref}>Sign in</a>
		{:else if onretry}
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
		flex: none;
		color: var(--status-danger);
		margin-top: 0.1rem;
	}
	.title {
		margin: 0;
		font-size: var(--text-md);
		line-height: 1.2;
	}
	.message {
		margin: var(--space-1) 0 0;
		overflow-wrap: anywhere;
	}
	.hint {
		margin: var(--space-1) 0 0;
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.details {
		margin: var(--space-2) 0 0;
		padding-left: var(--space-5);
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
		min-width: 0;
	}
	dd code {
		overflow-wrap: anywhere;
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
	a.retry {
		color: inherit;
		text-decoration: none;
	}
	.retry:hover {
		border-color: var(--ink-muted);
	}
</style>

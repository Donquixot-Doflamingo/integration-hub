<script lang="ts">
	import { resolve } from '$app/paths';
	import CopyButton from './CopyButton.svelte';
	import Icon from './Icon.svelte';

	/**
	 * Persistent success notice after a mutation (admin-console-ux §1): what changed, in the same
	 * words as the action, and the resulting change log id (copyable, linked to the change log).
	 * Stays until dismissed; announced politely, never steals focus.
	 */
	let {
		message,
		changeId,
		requestId = null,
		link,
		ondismiss
	}: {
		/** "Published v8. It is now the active version." */
		message: string;
		changeId: string | null;
		/** `X-Hub-Request-Id` of the call that made the change (hub-admin 1.1.0, HUB-278). */
		requestId?: string | null;
		/** A follow-up view of what changed, e.g. the replayed dead letters. */
		link?: { href: string; label: string };
		ondismiss?: () => void;
	} = $props();
</script>

<div class="notice" role="status">
	<Icon name="check" />
	<div class="body">
		<p class="message">{message}</p>
		{#if link}
			<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- callers pass resolved hrefs -->
			<p class="change"><a href={link.href}>{link.label}</a></p>
		{/if}
		{#if changeId}
			<p class="change">
				Change log id <code class="id">{changeId}</code>
				<CopyButton value={changeId} label="Copy change log id" />
				<a href={resolve('/(app)/change-log/[change_id]', { change_id: changeId })}
					>Open the change log entry</a
				>
			</p>
		{/if}
		{#if requestId}
			<p class="change">
				Request id <code class="id">{requestId}</code>
				<CopyButton value={requestId} label="Copy request id" />
			</p>
		{/if}
	</div>
	{#if ondismiss}
		<button type="button" class="dismiss" onclick={ondismiss} aria-label="Dismiss notice">
			<Icon name="close" size={16} />
		</button>
	{/if}
</div>

<style>
	.notice {
		display: flex;
		align-items: flex-start;
		gap: var(--space-3);
		margin: 0 0 var(--space-4);
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--status-ok);
		border-left-width: 4px;
		border-radius: var(--radius-md);
		background: var(--status-ok-wash);
		max-width: 72rem;
	}
	.notice > :global(.icon) {
		flex: none;
		color: var(--status-ok);
		margin-top: 0.1rem;
	}
	.body {
		flex: 1;
		min-width: 0;
	}
	p {
		margin: 0;
	}
	.message {
		font-weight: 700;
	}
	.change {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin-top: var(--space-1);
		font-size: var(--text-sm);
	}
	.change code {
		overflow-wrap: anywhere;
	}
	.dismiss {
		display: inline-grid;
		place-items: center;
		width: 1.75rem;
		height: 1.75rem;
		padding: 0;
		border: 1px solid transparent;
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--ink-muted);
		cursor: pointer;
	}
	.dismiss:hover {
		border-color: var(--rule-strong);
		color: var(--ink);
	}
</style>

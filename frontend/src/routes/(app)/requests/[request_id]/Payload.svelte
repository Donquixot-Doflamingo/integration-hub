<script lang="ts">
	import Icon from '$lib/components/Icon.svelte';
	import { formatBytes } from '$lib/utils/format';
	import { redactSecrets } from '$lib/utils/redact';
	import type { PayloadView } from './timeline';

	/**
	 * One payload under the audit policy (HUB-264, DESIGN §13.2): sealed payloads show a locked
	 * placeholder with size and hash, metadata-only shows that no body was stored, everything else is
	 * shown as the API returned it (masked values stay masked) behind a disclosure, with
	 * secret-looking fields hidden as a second line of defence. Never unmasked client side.
	 */
	let {
		view,
		bytes,
		sha256,
		label = 'payload'
	}: { view: PayloadView; bytes?: number; sha256?: string; label?: string } = $props();

	const text = $derived(
		view.kind === 'shown' ? JSON.stringify(redactSecrets(view.value), null, 2) : ''
	);
	const size = $derived(bytes === undefined ? '' : `${formatBytes(bytes)}`);
</script>

{#if view.kind === 'sealed'}
	<p class="locked">
		<Icon name="lock" size={16} />
		<span>
			<strong>Sealed {label}.</strong> Encrypted at rest{size ? `, ${size}` : ''}. Only an admin can
			unseal it, with a reason that is audited.
		</span>
	</p>
{:else if view.kind === 'metadata_only'}
	<p class="meta">Metadata only: the policy stores no body{size ? ` (${size} seen)` : ''}.</p>
{:else if view.kind === 'none'}
	<p class="meta">No {label}.</p>
{:else}
	<details>
		<summary>Show {label}{size ? ` (${size})` : ''}</summary>
		<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
		<pre tabindex="0" aria-label="{label} as JSON"><code>{text}</code></pre>
	</details>
{/if}
{#if sha256}
	<p class="hash">sha256 <code title={sha256}>{sha256.slice(0, 16)}...</code></p>
{/if}

<style>
	.locked {
		display: flex;
		align-items: flex-start;
		gap: var(--space-2);
		margin: 0;
		padding: var(--space-2) var(--space-3);
		border: 1px dashed var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface-sunken);
		font-size: var(--text-sm);
	}
	.meta,
	.hash {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	summary {
		cursor: pointer;
		font-size: var(--text-sm);
		font-weight: 600;
		color: var(--signal);
	}
	pre {
		max-height: 24rem;
		overflow: auto;
		margin: var(--space-2) 0 0;
		padding: var(--space-3);
		border: 1px solid var(--rule);
		border-radius: var(--radius-sm);
		background: var(--surface-sunken);
		font-size: var(--text-xs);
		line-height: 1.5;
	}
</style>

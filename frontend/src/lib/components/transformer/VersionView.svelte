<script lang="ts">
	import type { TransformerVersion } from '$lib/api/types';
	import type { Access } from '$lib/auth/roles';
	import { formatNumber, formatUtc } from '$lib/utils/format';
	import { redactSecrets } from '$lib/utils/redact';
	import CodeView from '../CodeView.svelte';
	import RoleGate from '../RoleGate.svelte';
	import StatusBadge from '../StatusBadge.svelte';
	import DryRun from './DryRun.svelte';
	import GoldensRunner from './GoldensRunner.svelte';

	/**
	 * One transformer version (HUB-268): who made it and why, the vendor doc it cites (link), its
	 * stored goldens result, the DSL source with syntax colours, the golden fixtures, and for
	 * editors a "Run goldens" control. Read only: editing
	 * transformers is Release 2 (F-19 non-goals). Editors also get the dry run (HUB-269).
	 */
	let { version: v, access }: { version: TransformerVersion; access: Access } = $props();

	const uid = $props.id();
	/** Only http(s) links are rendered as links (the contract says `format: uri`). */
	const docsUrl = $derived(v.docs_url && /^https?:\/\//i.test(v.docs_url) ? v.docs_url : null);
	const fixtureParts = [
		['input', 'Input (canonical request)'],
		['account_config', 'Account config'],
		['expected_request', 'Expected vendor request'],
		['vendor_response', 'Vendor response'],
		['expected_response', 'Expected canonical response'],
		['expected_error', 'Expected canonical error']
	] as const;
	const json = (value: unknown) => JSON.stringify(redactSecrets(value), null, 2);
</script>

<div class="version">
	<dl class="facts">
		<div>
			<dt>Version</dt>
			<dd><code>{v.ref}</code></dd>
		</div>
		<div>
			<dt>Kind</dt>
			<dd>{v.kind}</dd>
		</div>
		<div>
			<dt>Capability</dt>
			<dd><code>{v.capability}</code></dd>
		</div>
		<div>
			<dt>Vendor endpoint</dt>
			<dd><code>{v.vendor_endpoint}</code></dd>
		</div>
		<div>
			<dt>Author</dt>
			<dd>{v.author}</dd>
		</div>
		<div>
			<dt>Created</dt>
			<dd>{formatUtc(v.created_at)}</dd>
		</div>
		<div class="wide">
			<dt>Reason</dt>
			<dd>{v.reason}</dd>
		</div>
		<div class="wide">
			<dt>Vendor documentation</dt>
			<dd>
				{#if docsUrl}
					<a href={docsUrl} target="_blank" rel="noopener noreferrer external"
						>{docsUrl}<span class="visually-hidden"> (opens in a new tab)</span></a
					>
				{:else if v.docs_url}
					<code>{v.docs_url}</code>
				{:else}
					<span class="muted">None cited</span>
				{/if}
			</dd>
		</div>
		<div>
			<dt>Stored goldens</dt>
			<dd>
				<StatusBadge
					tone={v.goldens.passed ? 'ok' : 'danger'}
					icon={v.goldens.passed ? 'closed' : 'open'}
					label={v.goldens.passed ? 'Passing' : 'Failing'}
				/>
				{formatNumber(v.goldens.total - v.goldens.failed)} of {formatNumber(v.goldens.total)}
			</dd>
		</div>
	</dl>

	<section aria-labelledby="{uid}-goldens">
		<h3 id="{uid}-goldens">Goldens</h3>
		<RoleGate {access} op="runTransformerGoldens">
			{#key v.ref}<GoldensRunner transformerRef={v.ref} />{/key}
			{#snippet denied(role)}
				<p class="muted">Running goldens needs the {role} role. The stored result is above.</p>
			{/snippet}
		</RoleGate>
	</section>

	<section aria-labelledby="{uid}-dry-run">
		<h3 id="{uid}-dry-run">Dry run</h3>
		<RoleGate {access} op="dryRunTransformer">
			{#key v.ref}<DryRun version={v} />{/key}
			{#snippet denied(role)}
				<p class="muted">A dry run needs the {role} role.</p>
			{/snippet}
		</RoleGate>
	</section>

	<section aria-labelledby="{uid}-source">
		<h3 id="{uid}-source">Source</h3>
		<CodeView source={v.yaml} label="{v.ref} source (YAML)" />
	</section>

	<section aria-labelledby="{uid}-fixtures">
		<h3 id="{uid}-fixtures">
			Golden fixtures <span class="muted">({formatNumber(v.fixtures.length)})</span>
		</h3>
		{#if v.fixtures.length === 0}
			<p class="muted">No fixtures returned.</p>
		{:else}
			<ul class="fixtures">
				{#each v.fixtures as f (f.name)}
					<li>
						<details>
							<summary><code>{f.name}</code></summary>
							{#each fixtureParts as [key, label] (key)}
								{#if f[key] !== undefined}
									<p class="part">{label}</p>
									<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
									<pre tabindex="0" aria-label="{f.name}: {label}">{json(f[key])}</pre>
								{/if}
							{/each}
						</details>
					</li>
				{/each}
			</ul>
		{/if}
	</section>
</div>

<style>
	.version {
		display: grid;
		gap: var(--space-5);
		max-width: 72rem;
	}
	.wide {
		grid-column: 1 / -1;
	}
	h3 {
		font-size: var(--text-lg);
		margin-bottom: var(--space-2);
	}
	.fixtures {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}
	summary {
		cursor: pointer;
		font-weight: 600;
		color: var(--signal);
	}
	.part {
		margin: var(--space-2) 0 var(--space-1);
		color: var(--ink-muted);
		font-size: var(--text-xs);
		font-weight: 600;
	}
	pre {
		max-height: 18rem;
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

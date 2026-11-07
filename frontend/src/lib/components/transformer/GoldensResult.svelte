<script lang="ts">
	import type { GoldensRunResult } from '$lib/api/types';
	import { formatDurationMs, formatNumber } from '$lib/utils/format';
	import { summarize } from '$lib/utils/diff';
	import DiffPaths from '../diff/DiffPaths.svelte';
	import StatusBadge from '../StatusBadge.svelte';

	/**
	 * Result of `runTransformerGoldens` (HUB-268, HUB-436): a summary line, then one block per
	 * sample with pass/fail in text + icon, the error when it threw, its diff as a path list
	 * (expected on the left, what the transformer produced on the right) and, for request samples,
	 * every field of the produced request that breaks the vendor endpoint's request schema
	 * (`schema_errors`: the JSON pointer of the field, the keyword and the reason). Failing samples
	 * come first.
	 */
	let { result, refLabel }: { result: GoldensRunResult; refLabel: string } = $props();

	const ordered = $derived([...result.results].sort((a, b) => Number(a.passed) - Number(b.passed)));
	const uid = $props.id();
</script>

<div class="goldens">
	<p class="summary" role="status">
		<StatusBadge
			tone={result.passed ? 'ok' : 'danger'}
			icon={result.passed ? 'closed' : 'open'}
			label={result.passed ? 'All samples pass' : 'Samples fail'}
		/>
		<span>
			{refLabel}: {formatNumber(result.total - result.failed)} of {formatNumber(result.total)}
			{result.total === 1 ? 'sample passes' : 'samples pass'}{result.failed
				? `, ${formatNumber(result.failed)} failing`
				: ''}{typeof result.duration_ms === 'number'
				? ` (${formatDurationMs(result.duration_ms)})`
				: ''}.
		</span>
	</p>
	<ol class="fixtures">
		{#each ordered as r, i (r.fixture)}
			{@const violations = r.schema_errors ?? []}
			<li class:failed={!r.passed} aria-labelledby="{uid}-f{i}">
				<h4 id="{uid}-f{i}">
					<code>{r.fixture}</code>
					<StatusBadge
						tone={r.passed ? 'ok' : 'danger'}
						icon={r.passed ? 'check' : 'open'}
						label={r.passed ? 'Passed' : 'Failed'}
					/>
				</h4>
				{#if r.error}
					<p class="error"><strong>Error:</strong> {r.error}</p>
				{/if}
				{#if violations.length}
					<div
						class="violations"
						role="group"
						aria-label="Endpoint schema violations of {r.fixture}"
					>
						<p class="part">
							The produced request breaks the endpoint's request schema ({violations.length}
							{violations.length === 1 ? 'field' : 'fields'}):
						</p>
						<ul>
							{#each violations as v, k (k)}
								<li>
									<code class="field">{v.field}</code>
									{#if v.keyword}<span class="kw">{v.keyword}</span>{/if}
									<span>{v.message}</span>
								</li>
							{/each}
						</ul>
					</div>
				{/if}
				{#if r.diff.length}
					<p class="diff-summary">Differs from the expected output: {summarize(r.diff)}</p>
					<DiffPaths
						changes={r.diff}
						label="Differences for sample {r.fixture}"
						beforeLabel="Expected"
						afterLabel="Actual"
						rootLabel="(whole output)"
					/>
				{:else if !r.passed && !r.error && !violations.length}
					<p class="muted">Failed without a diff or an error message.</p>
				{:else if r.passed}
					<p class="muted">Output matches the expected one.</p>
				{/if}
			</li>
		{/each}
	</ol>
</div>

<style>
	.goldens {
		display: grid;
		gap: var(--space-3);
	}
	.summary {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin: 0;
		font-weight: 600;
	}
	.fixtures {
		display: grid;
		gap: var(--space-3);
		margin: 0;
		padding: 0;
		list-style: none;
	}
	.fixtures > li {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--rule);
		border-left: 4px solid var(--status-ok);
		border-radius: var(--radius-sm);
		background: var(--surface);
	}
	.fixtures > li.failed {
		border-left-color: var(--status-danger);
		border-left-width: 6px;
	}
	h4 {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		margin: 0 0 var(--space-1);
		font-size: var(--text-sm);
	}
	.error {
		margin: 0 0 var(--space-2);
		color: var(--status-danger);
		font-size: var(--text-sm);
		overflow-wrap: anywhere;
	}
	.violations {
		margin: 0 0 var(--space-2);
		padding: var(--space-2) var(--space-3);
		border-left: 3px solid var(--status-danger);
		background: var(--status-danger-wash);
		font-size: var(--text-sm);
	}
	.violations ul {
		display: grid;
		gap: 0.15rem;
		margin: 0;
		padding-left: var(--space-4);
	}
	.violations li {
		overflow-wrap: anywhere;
	}
	.field {
		font-weight: 700;
	}
	.kw {
		margin: 0 0.35rem;
		padding: 0 0.3rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		font-size: var(--text-xs);
	}
	.part {
		margin: 0 0 var(--space-1);
		font-weight: 600;
	}
	.diff-summary,
	.muted {
		margin: 0 0 var(--space-2);
		font-size: var(--text-sm);
	}
	.diff-summary {
		overflow-wrap: anywhere;
	}
	.muted {
		color: var(--ink-muted);
	}
</style>

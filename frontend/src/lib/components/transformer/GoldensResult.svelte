<script lang="ts">
	import type { GoldensRunResult } from '$lib/api/types';
	import { formatDurationMs, formatNumber } from '$lib/utils/format';
	import { summarize } from '$lib/utils/diff';
	import DiffPaths from '../diff/DiffPaths.svelte';
	import StatusBadge from '../StatusBadge.svelte';

	/**
	 * Result of `runTransformerGoldens` (HUB-268): a summary line, then one block per fixture with
	 * pass/fail in text + icon, the error when it threw, and its diff as a path list (expected on
	 * the left, what the transformer produced on the right). Failing fixtures come first.
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
			label={result.passed ? 'All goldens pass' : 'Goldens fail'}
		/>
		<span>
			{refLabel}: {formatNumber(result.total - result.failed)} of {formatNumber(result.total)}
			{result.total === 1 ? 'fixture passes' : 'fixtures pass'}{result.failed
				? `, ${formatNumber(result.failed)} failing`
				: ''}{typeof result.duration_ms === 'number'
				? ` (${formatDurationMs(result.duration_ms)})`
				: ''}.
		</span>
	</p>
	<ol class="fixtures">
		{#each ordered as r, i (r.fixture)}
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
				{#if r.diff.length}
					<p class="diff-summary">{summarize(r.diff)}</p>
					<DiffPaths
						changes={r.diff}
						label="Differences for fixture {r.fixture}"
						beforeLabel="Expected"
						afterLabel="Actual"
						rootLabel="(whole output)"
					/>
				{:else if !r.passed && !r.error}
					<p class="muted">Failed without a diff or an error message.</p>
				{:else if r.passed}
					<p class="muted">Output matches the golden.</p>
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
	.fixtures li {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--rule);
		border-left: 4px solid var(--status-ok);
		border-radius: var(--radius-sm);
		background: var(--surface);
	}
	.fixtures li.failed {
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
	.diff-summary,
	.muted {
		margin: 0 0 var(--space-2);
		font-size: var(--text-sm);
	}
	.muted {
		color: var(--ink-muted);
	}
</style>

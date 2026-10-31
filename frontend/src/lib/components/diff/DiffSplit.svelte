<script lang="ts">
	import { SvelteSet } from 'svelte/reactivity';
	import { foldUnchanged, splitUnified, type Side, type SplitRow } from '$lib/utils/diff';

	/**
	 * Side-by-side diff (admin-console-ux §2): old on the left, new on the right, from the unified
	 * YAML diff the admin API returns. Changed lines carry a symbol (- / +), a hidden text label and
	 * a colour; long unchanged runs fold behind a button.
	 */
	let { unified, oldLabel, newLabel }: { unified: string; oldLabel: string; newLabel: string } =
		$props();

	const split = $derived(splitUnified(unified));
	const rows = $derived(foldUnchanged(split.rows));
	const open = new SvelteSet<number>();

	const symbol = (s: Side | null) => (s?.type === 'removed' ? '-' : s?.type === 'added' ? '+' : '');
	const srLabel = (s: Side | null) =>
		s?.type === 'removed' ? 'Removed: ' : s?.type === 'added' ? 'Added: ' : '';
</script>

{#snippet line(r: SplitRow)}
	{#if r.kind === 'hunk'}
		<tr class="hunk">
			<td colspan="4">{r.text}</td>
		</tr>
	{:else if r.kind === 'line'}
		<tr>
			<td class="sym {r.left?.type ?? 'empty'}" aria-hidden="true">{symbol(r.left)}</td>
			<td class="code {r.left?.type ?? 'empty'}">
				{#if r.left}<span class="visually-hidden">{srLabel(r.left)}</span>{r.left.text}{/if}
			</td>
			<td class="sym {r.right?.type ?? 'empty'}" aria-hidden="true">{symbol(r.right)}</td>
			<td class="code {r.right?.type ?? 'empty'}">
				{#if r.right}<span class="visually-hidden">{srLabel(r.right)}</span>{r.right.text}{/if}
			</td>
		</tr>
	{/if}
{/snippet}

{#if split.rows.length === 0}
	<p class="none">The two versions render to the same YAML.</p>
{:else}
	<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
	<div class="scroll" role="region" aria-label="Side-by-side diff" tabindex="0">
		<table>
			<caption class="visually-hidden">
				{oldLabel} on the left, {newLabel} on the right
			</caption>
			<colgroup>
				<col class="sym-col" /><col /><col class="sym-col" /><col />
			</colgroup>
			<thead>
				<tr>
					<th scope="col" colspan="2">{oldLabel}</th>
					<th scope="col" colspan="2">{newLabel}</th>
				</tr>
			</thead>
			<tbody>
				{#each rows as r, i (i)}
					{#if r.kind === 'fold'}
						{#if open.has(r.id)}
							{#each r.rows as inner, j (j)}{@render line(inner)}{/each}
						{:else}
							<tr class="fold">
								<td colspan="4">
									<button type="button" onclick={() => open.add(r.id)}>
										Show {r.rows.length} unchanged lines
									</button>
								</td>
							</tr>
						{/if}
					{:else}
						{@render line(r)}
					{/if}
				{/each}
			</tbody>
		</table>
	</div>
{/if}

<style>
	.scroll {
		overflow-x: auto;
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
	}
	table {
		width: 100%;
		border-collapse: collapse;
		table-layout: fixed;
		font-family: var(--font-id);
		font-size: var(--text-sm);
	}
	.sym-col {
		width: 1.75rem;
	}
	thead th {
		padding: var(--space-2) var(--space-3);
		background: var(--surface-sunken);
		color: var(--ink-muted);
		font-family: var(--font-ui);
		font-size: var(--text-xs);
		text-align: left;
		border-bottom: 1px solid var(--rule);
	}
	thead th + th {
		border-left: 1px solid var(--rule);
	}
	td {
		padding: 0.1rem var(--space-2);
		vertical-align: top;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	td.sym {
		text-align: center;
		font-weight: 700;
		user-select: none;
	}
	td.sym:nth-child(3) {
		border-left: 1px solid var(--rule);
	}
	.removed {
		background: var(--status-danger-wash);
	}
	.sym.removed {
		color: var(--status-danger);
	}
	.added {
		background: var(--status-ok-wash);
	}
	.sym.added {
		color: var(--status-ok);
	}
	.empty {
		background: repeating-linear-gradient(-45deg, transparent 0 6px, var(--surface-sunken) 6px 8px);
	}
	.hunk td {
		padding: var(--space-1) var(--space-3);
		background: var(--signal-wash);
		color: var(--ink-muted);
		font-family: var(--font-ui);
		font-size: var(--text-xs);
		font-weight: 600;
	}
	.fold td {
		padding: var(--space-1) var(--space-3);
		background: var(--surface-sunken);
		font-family: var(--font-ui);
	}
	.fold button {
		padding: 0;
		border: 0;
		background: none;
		color: var(--signal);
		font-size: var(--text-xs);
		font-weight: 600;
		text-decoration: underline;
		cursor: pointer;
	}
	.none {
		color: var(--ink-muted);
	}
</style>

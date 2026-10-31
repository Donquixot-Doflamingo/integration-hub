<script lang="ts">
	import type { DiffOp } from '$lib/api/types';
	import { formatValue, opLabels, pointerToPath } from '$lib/utils/diff';

	/** Path list (HUB-258): one row per changed JSON pointer with before and after values. */
	let { changes }: { changes: DiffOp[] } = $props();
</script>

{#if changes.length === 0}
	<p class="none">No differences.</p>
{:else}
	<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
	<div class="scroll" role="region" aria-label="Changed paths" tabindex="0">
		<table>
			<caption class="visually-hidden">Changed paths with values before and after</caption>
			<thead>
				<tr>
					<th scope="col">Path</th>
					<th scope="col">Change</th>
					<th scope="col">Before</th>
					<th scope="col">After</th>
				</tr>
			</thead>
			<tbody>
				{#each changes as c, i (i)}
					<tr class={c.op}>
						<th scope="row">
							<code class="path">{pointerToPath(c.path)}</code>
							<code class="pointer">{c.path}</code>
						</th>
						<td class="op"
							><span aria-hidden="true">{opLabels[c.op].symbol}</span> {opLabels[c.op].label}</td
						>
						<td class="before">
							{#if c.op === 'add'}<span class="absent">Not present</span>{:else}<pre>{formatValue(
										c.before
									)}</pre>{/if}
						</td>
						<td class="after">
							{#if c.op === 'remove'}<span class="absent">Not present</span
								>{:else}<pre>{formatValue(c.after)}</pre>{/if}
						</td>
					</tr>
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
		font-size: var(--text-sm);
	}
	th,
	td {
		padding: var(--space-2) var(--space-3);
		text-align: left;
		vertical-align: top;
		border-bottom: 1px solid var(--rule);
	}
	tbody tr:last-child > * {
		border-bottom: 0;
	}
	thead th {
		background: var(--surface-sunken);
		color: var(--ink-muted);
		font-size: var(--text-xs);
		font-weight: 600;
	}
	tbody th {
		font-weight: 400;
		border-left: 4px solid transparent;
	}
	.add th {
		border-left-color: var(--status-ok);
	}
	.remove th {
		border-left-color: var(--status-danger);
	}
	.replace th {
		border-left-color: var(--status-warn);
	}
	.path {
		display: block;
		font-weight: 700;
	}
	.pointer {
		display: block;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.op {
		white-space: nowrap;
		font-weight: 600;
	}
	.add .op {
		color: var(--status-ok);
	}
	.remove .op {
		color: var(--status-danger);
	}
	.replace .op {
		color: var(--status-warn);
	}
	pre {
		margin: 0;
		font-family: var(--font-id);
		font-size: var(--text-sm);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.before pre {
		background: var(--status-danger-wash);
	}
	.after pre {
		background: var(--status-ok-wash);
	}
	.absent,
	.none {
		color: var(--ink-muted);
		font-style: italic;
	}
</style>

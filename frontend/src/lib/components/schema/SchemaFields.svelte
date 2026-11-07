<script lang="ts">
	import { fieldRows } from '$lib/utils/json-schema';

	/**
	 * A JSON Schema document as a readable field list (HUB-436): path, type, Required / Optional
	 * in text, description, PII tag and other constraints. Read only (integration and endpoint
	 * detail pages). `empty` is what to say when the document declares no field.
	 */
	let {
		schema,
		label,
		empty = 'No fields declared.'
	}: { schema: unknown; label: string; empty?: string } = $props();

	const rows = $derived(fieldRows(schema));
</script>

{#if rows.length}
	<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
	<div class="wrap" role="region" aria-label={label} tabindex="0">
		<table>
			<thead>
				<tr>
					<th scope="col">Field</th>
					<th scope="col">Type</th>
					<th scope="col">Required</th>
					<th scope="col">Notes</th>
				</tr>
			</thead>
			<tbody>
				{#each rows as r (r.path)}
					<tr>
						<th scope="row" style="--depth: {r.depth}"><code>{r.path}</code></th>
						<td>{r.type}</td>
						<td>
							<span class="badge" class:req={r.required}
								>{r.required ? 'Required' : 'Optional'}</span
							>
						</td>
						<td>
							<div class="notes">
								{#if r.description}<span>{r.description}</span>{/if}
								{#if r.pii}<span class="tag">PII: {r.pii}</span>{/if}
								{#each r.constraints as c (c)}<code class="rule">{c}</code>{/each}
							</div>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{:else}
	<p class="none">{empty}</p>
{/if}

<style>
	.wrap {
		overflow-x: auto;
		border: 1px solid var(--rule);
		border-radius: var(--radius-sm);
		background: var(--surface);
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: var(--text-sm);
	}
	th,
	td {
		height: var(--row-height);
		padding: 0.15rem var(--space-3);
		text-align: left;
		border-bottom: 1px solid var(--rule);
		vertical-align: middle;
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
		padding-left: calc(var(--space-3) + var(--depth, 0) * 1.1rem);
		font-weight: 400;
		white-space: nowrap;
	}
	.badge {
		padding: 0 0.35rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		color: var(--ink-muted);
		font-size: var(--text-xs);
		font-weight: 600;
	}
	.badge.req {
		border-color: var(--signal);
		color: var(--signal);
	}
	.notes {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.2rem var(--space-2);
	}
	.tag,
	.rule {
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.none {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
</style>

<script lang="ts">
	import type { ApiResult } from '$lib/api/client';
	import { consoleApi } from '$lib/api/console';
	import type { SnapshotDetail } from '$lib/api/types';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import { objectHref } from '$lib/objects';
	import { objectTypeLabels } from '../change-log/filters';

	/**
	 * Expandable diff summary of one snapshot (HUB-273): on first open it reads `getSnapshot` (the
	 * objects changed versus the previous snapshot) and lists them; closing keeps the answer.
	 */
	let { version }: { version: number } = $props();

	const id = $props.id();
	let open = $state(false);
	let detail = $state.raw<Promise<ApiResult<SnapshotDetail>> | null>(null);
	const changeLabels = { created: 'Created', updated: 'Updated', deleted: 'Deleted' } as const;

	function toggle() {
		open = !open;
		if (open && !detail) {
			const api = consoleApi({ fetch, url: new URL(window.location.href) });
			detail = api.call((c) =>
				c.GET('/snapshots/{snapshot_version}', {
					params: { path: { snapshot_version: version } }
				})
			);
		}
	}
	function retry() {
		detail = null;
		open = false;
		toggle();
	}
</script>

<button
	type="button"
	class="toggle"
	aria-expanded={open}
	aria-controls="{id}-panel"
	onclick={toggle}
>
	<Icon name="chevron" size={14} />{open ? 'Hide' : 'Show'} changes in {version}
</button>
<div id="{id}-panel" class="panel" hidden={!open}>
	{#if detail}
		<AsyncView
			result={detail}
			noun="changes in snapshot {version}"
			onretry={retry}
			headingLevel={3}
		>
			{#snippet children(d)}
				<p class="from">
					{d.previous_version == null
						? 'First snapshot.'
						: `Versus snapshot ${d.previous_version}:`}
				</p>
				{#if d.changed_objects.length === 0}
					<p class="from">No registry objects changed.</p>
				{:else}
					<ul>
						{#each d.changed_objects as o, i (i)}
							{@const href = objectHref(o.object_type, o.object_id)}
							<li>
								<span class="change {o.change}">{changeLabels[o.change]}</span>
								{objectTypeLabels[o.object_type].toLowerCase()}
								<!-- eslint-disable svelte/no-navigation-without-resolve -- objectHref() builds on resolve() -->
								{#if href}<a {href}><code class="id">{o.object_id}</code></a>{:else}<code class="id"
										>{o.object_id}</code
									>{/if}
								<!-- eslint-enable svelte/no-navigation-without-resolve -->
							</li>
						{/each}
					</ul>
				{/if}
			{/snippet}
		</AsyncView>
	{/if}
</div>

<style>
	.toggle {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		padding: 0.15rem 0.6rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-xs);
		font-weight: 700;
		cursor: pointer;
	}
	.toggle[aria-expanded='true'] :global(.icon) {
		transform: rotate(90deg);
	}
	.panel {
		margin-top: var(--space-1);
		white-space: normal;
		font-size: var(--text-sm);
	}
	.from {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	ul {
		margin: var(--space-1) 0 var(--space-2);
		padding-left: var(--space-4);
	}
	.change {
		font-weight: 700;
	}
</style>

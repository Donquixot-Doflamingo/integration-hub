<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import DiffPaths from '$lib/components/diff/DiffPaths.svelte';
	import { summarize } from '$lib/utils/diff';
	import { formatUtc } from '$lib/utils/format';
	import { jsonDiff } from '$lib/utils/json-diff';
	import type { PageData } from './$types';
	import { objectTypeLabels } from '../filters';
	import { objectHref } from '../objects';

	let { data }: { data: PageData } = $props();
	const id = $derived(data.changeId);
	const listHref = (q: string) => `${resolve('/change-log')}?${q}`;
	const pretty = (v: unknown) => (v == null ? 'None' : JSON.stringify(v, null, 2));
</script>

<PageHeader title={id} />

<AsyncView result={data.entry} noun="change {id}" onretry={invalidateAll}>
	{#snippet children(c)}
		{@const object = objectHref(c.object_type, c.object_id)}
		{@const changes = jsonDiff(c.before, c.after)}
		<section class="head" aria-label="Summary">
			<p class="what">
				<code>{c.action}</code> on {objectTypeLabels[c.object_type].toLowerCase()}
				<!-- eslint-disable svelte/no-navigation-without-resolve -- objectHref() builds on resolve() -->
				{#if object}<a href={object}><code class="id">{c.object_id}</code></a>{:else}<code
						class="id">{c.object_id}</code
					>{/if}
				<!-- eslint-enable svelte/no-navigation-without-resolve -->
			</p>
			<blockquote class="reason">
				<p><span class="visually-hidden">Reason: </span>{c.reason}</p>
			</blockquote>
			<dl class="facts">
				<div>
					<dt>When</dt>
					<dd>{formatUtc(c.at)}</dd>
				</div>
				<div>
					<dt>Actor</dt>
					<dd>
						<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- built on resolve() -->
						<a href={listHref(`actor=${encodeURIComponent(c.actor)}`)}>{c.actor}</a>
						{#if c.actor_roles?.length}({c.actor_roles.join(', ')}){/if}
					</dd>
				</div>
				<div>
					<dt>Snapshot</dt>
					<dd>
						{c.snapshot_version == null ? 'None (no registry change)' : c.snapshot_version}
					</dd>
				</div>
				<div>
					<dt>Change id</dt>
					<dd>
						<code class="id">{c.change_id}</code>
						<CopyButton value={c.change_id} label="Copy change id" />
					</dd>
				</div>
			</dl>
			<p class="meta">
				<!-- eslint-disable svelte/no-navigation-without-resolve -- built on resolve() -->
				<a
					href={listHref(
						`object_type=${c.object_type}&object_id=${encodeURIComponent(c.object_id)}`
					)}>Every change to {c.object_id}</a
				>
				<!-- eslint-enable svelte/no-navigation-without-resolve -->
			</p>
		</section>

		<section class="block" aria-labelledby="diff-heading">
			<h2 id="diff-heading">Before and after</h2>
			<p class="meta">
				{c.before == null
					? 'Created: there was no object before this change.'
					: c.after == null
						? 'Deleted: there is no object after this change.'
						: summarize(changes)}
				Secrets never appear here (write-only fields are recorded as a fingerprint).
			</p>
			<DiffPaths {changes} label="Changed fields" rootLabel="(whole object)" />
			<details class="full">
				<summary>Full before and after</summary>
				<div class="sides">
					<figure>
						<figcaption>Before</figcaption>
						<pre>{pretty(c.before)}</pre>
					</figure>
					<figure>
						<figcaption>After</figcaption>
						<pre>{pretty(c.after)}</pre>
					</figure>
				</div>
			</details>
		</section>
	{/snippet}
</AsyncView>

<style>
	.head {
		display: grid;
		gap: var(--space-3);
		max-width: 72rem;
		padding-bottom: var(--space-4);
		border-bottom: 1px solid var(--rule);
	}
	.what {
		margin: 0;
		font-weight: 600;
	}
	.reason {
		margin: 0;
		padding: var(--space-2) var(--space-3);
		border-left: 4px solid var(--signal);
		background: var(--signal-wash);
	}
	.reason p {
		margin: 0;
	}
	.block {
		margin-top: var(--space-5);
		max-width: 72rem;
	}
	h2 {
		font-size: var(--text-lg);
	}
	.meta {
		margin: var(--space-1) 0 var(--space-3);
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.head .meta {
		margin: 0;
	}
	.full {
		margin-top: var(--space-3);
	}
	.full summary {
		cursor: pointer;
		font-weight: 600;
	}
	.sides {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(18rem, 1fr));
		gap: var(--space-3);
		margin-top: var(--space-2);
	}
	figure {
		margin: 0;
		min-width: 0;
	}
	figcaption {
		font-size: var(--text-xs);
		font-weight: 700;
		color: var(--ink-muted);
	}
	pre {
		margin: var(--space-1) 0 0;
		padding: var(--space-3);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		border: 1px solid var(--rule);
		border-radius: var(--radius-sm);
		background: var(--surface-sunken);
		font-size: var(--text-xs);
	}
</style>

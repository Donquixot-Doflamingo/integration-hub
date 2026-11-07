<script lang="ts">
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import Icon from '$lib/components/Icon.svelte';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import Pager from '$lib/components/Pager.svelte';
	import { sectionFor } from '$lib/nav';
	import { formatNumber, formatUtc } from '$lib/utils/format';
	import { DEFAULT_PAGE_SIZE, PAGE_SIZES, type Paging } from '$lib/utils/paging';
	import type { PageData } from './$types';
	import {
		chipValue,
		FILTER_KEYS,
		filterLabels,
		filtersFromForm,
		isFiltered,
		KINDS,
		kindLabels,
		searchFor,
		without,
		type Filters
	} from './filters';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/transformers')!;

	const filters = $derived(data.state.filters);
	const paging = $derived(data.state.paging);
	const filtered = $derived(isFiltered(filters));
	const base = resolve('/transformers');
	const firstPage = (limit: number): Paging => ({ cursor: undefined, prev: [], limit });
	const hrefFor = (f: Filters, p: Paging = firstPage(paging.limit)) => base + searchFor(f, p);
	const chips = $derived(FILTER_KEYS.filter((k) => !!filters[k]));

	function onsubmit(e: SubmitEvent) {
		e.preventDefault();
		const { filters: next, limit } = filtersFromForm(
			new FormData(e.currentTarget as HTMLFormElement)
		);
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- hrefFor builds on resolve()
		goto(hrefFor(next, firstPage(limit ?? DEFAULT_PAGE_SIZE)), { keepFocus: true, noScroll: true });
	}
</script>

<PageHeader title={section.label} summary={section.summary} />

<RoleGate access={data.access} ops={['createTransformerVersion', 'runTransformerGoldens']}>
	<p class="create">
		<a class="btn btn-primary" href={resolve('/(app)/transformers/new')}>New transformer</a>
	</p>
</RoleGate>

{#if data.access.tenantScoped && data.access.known}
	<p class="note" role="note">
		Transformers map to vendor APIs, which tenant viewers cannot see. Ask the platform team about a
		mapping.
	</p>
{:else}
	<form class="filters" method="GET" action={base} {onsubmit} aria-label="Filter transformers">
		<div class="field grow">
			<label for="t-q">Search</label>
			<input
				id="t-q"
				name="q"
				type="search"
				value={filters.q ?? ''}
				maxlength="128"
				placeholder="transformer id"
			/>
		</div>
		<div class="field">
			<label for="t-kind">Kind</label>
			<select id="t-kind" name="kind" value={filters.kind ?? ''}>
				<option value="">Any</option>
				{#each KINDS as k (k)}<option value={k}>{kindLabels[k]}</option>{/each}
			</select>
		</div>
		<div class="field">
			<label for="t-vendor">Vendor</label>
			<input
				id="t-vendor"
				name="vendor_id"
				value={filters.vendor_id ?? ''}
				maxlength="64"
				placeholder="clevertap"
				autocomplete="off"
			/>
		</div>
		<div class="field">
			<label for="t-limit">Per page</label>
			<select id="t-limit" name="limit" value={String(paging.limit)}>
				{#each PAGE_SIZES as n (n)}<option value={String(n)}>{n}</option>{/each}
			</select>
		</div>
		<button type="submit" class="apply">Apply</button>
	</form>

	{#if chips.length}
		<div class="chips">
			<span class="chips-label" id="active-filters">Active filters</span>
			<ul aria-labelledby="active-filters">
				{#each chips as key (key)}
					<li>
						<!-- eslint-disable svelte/no-navigation-without-resolve -- hrefFor builds on resolve() -->
						<a
							class="chip"
							href={hrefFor(without(filters, key))}
							aria-label="Remove filter {filterLabels[key]}: {chipValue(key, filters[key]!)}"
						>
							<span class="chip-key">{filterLabels[key]}</span>
							{chipValue(key, filters[key]!)}
							<Icon name="close" size={14} />
						</a>
						<!-- eslint-enable svelte/no-navigation-without-resolve -->
					</li>
				{/each}
			</ul>
			<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- hrefFor builds on resolve() -->
			<a class="clear" href={hrefFor({})}>Clear all</a>
		</div>
	{/if}

	<ListView
		result={data.list}
		noun="transformers"
		caption="Transformers"
		emptyTitle={filtered ? 'No transformers match these filters' : 'No transformers yet'}
		{filtered}
		clearHref={hrefFor({})}
		columns={[
			{ label: 'Transformer' },
			{ label: 'Kind' },
			{ label: 'Vendor endpoint' },
			{ label: 'Latest version', numeric: true },
			{ label: 'Used by active versions', numeric: true },
			{ label: 'Updated' }
		]}
		onretry={invalidateAll}
	>
		{#snippet row(t)}
			<th scope="row">
				<a
					href={resolve('/(app)/transformers/[transformer_id]', {
						transformer_id: t.transformer_id
					})}><code class="id">{t.transformer_id}</code></a
				>
			</th>
			<td>{t.kind}</td>
			<td>
				{#if t.vendor_endpoint}<code>{t.vendor_endpoint}</code>{:else}<span class="muted"
						>None (response transformers stand alone)</span
					>{/if}
			</td>
			<td class="numeric">{t.latest_version == null ? 'None' : `v${t.latest_version}`}</td>
			<td class="numeric">{formatNumber(t.used_by_active_versions)}</td>
			<td>{formatUtc(t.updated_at)}</td>
		{/snippet}
		{#snippet empty()}
			{#if filtered}
				<p>Nothing matches. Remove a filter or clear them all.</p>
			{:else}
				<p>
					Transformers are added by the platform team through pull requests with golden fixtures.
				</p>
			{/if}
		{/snippet}
		{#snippet footer(page)}
			<Pager
				{paging}
				nextCursor={page.next_cursor}
				count={page.items.length}
				noun="transformers"
				href={(p) => hrefFor(filters, p)}
			/>
		{/snippet}
	</ListView>
{/if}

<style>
	.create {
		margin: 0 0 var(--space-3);
	}
	.note {
		color: var(--ink-muted);
		max-width: 46rem;
	}
	.filters {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: var(--space-3);
		margin-bottom: var(--space-3);
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
	}
	.field {
		display: grid;
		gap: 0.2rem;
		min-width: 9rem;
	}
	.field.grow {
		flex: 1 1 14rem;
	}
	label {
		font-size: var(--text-xs);
		font-weight: 600;
		color: var(--ink-muted);
	}
	input,
	select {
		height: 2rem;
		padding: 0 0.5rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-sm);
	}
	.apply {
		height: 2rem;
		padding: 0 1rem;
		border: 1px solid var(--signal);
		border-radius: var(--radius-sm);
		background: var(--signal);
		color: var(--surface);
		font-weight: 700;
		cursor: pointer;
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin-bottom: var(--space-3);
		font-size: var(--text-sm);
	}
	.chips-label {
		color: var(--ink-muted);
	}
	.chips ul {
		display: contents;
		list-style: none;
	}
	.chips li {
		display: inline-flex;
	}
	.chip {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		padding: 0.1rem 0.4rem 0.1rem 0.6rem;
		border: 1px solid var(--signal);
		border-radius: 999px;
		background: var(--signal-wash);
		color: var(--ink);
		text-decoration: none;
	}
	.chip:hover {
		text-decoration: line-through;
	}
	.chip-key {
		color: var(--ink-muted);
	}
	.clear {
		font-weight: 600;
	}
</style>

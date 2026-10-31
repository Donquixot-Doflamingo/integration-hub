<script lang="ts">
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { IntegrationSummary } from '$lib/api/types';
	import Icon from '$lib/components/Icon.svelte';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Pager from '$lib/components/Pager.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { sectionFor } from '$lib/nav';
	import { formatUtc } from '$lib/utils/format';
	import { DEFAULT_PAGE_SIZE, PAGE_SIZES, type Paging } from '$lib/utils/paging';
	import type { PageData } from './$types';
	import {
		chipValue,
		FILTER_KEYS,
		filterLabels,
		filtersFromForm,
		isFiltered,
		searchFor,
		without,
		type Filters
	} from './filters';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/integrations')!;

	const filters = $derived(data.state.filters);
	const paging = $derived(data.state.paging);
	const access = $derived(data.access);
	const filtered = $derived(isFiltered(filters));
	const base = resolve('/integrations');
	const firstPage = (limit: number): Paging => ({ cursor: undefined, prev: [], limit });
	const hrefFor = (f: Filters, p: Paging = firstPage(paging.limit)) => base + searchFor(f, p);
	const chips = $derived(FILTER_KEYS.filter((k) => !!filters[k]));

	function onsubmit(e: SubmitEvent) {
		// Without JS the browser submits the GET form as is; with JS, drop empty fields from the URL.
		e.preventDefault();
		const { filters: next, limit } = filtersFromForm(
			new FormData(e.currentTarget as HTMLFormElement)
		);
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- hrefFor builds on resolve()
		goto(hrefFor(next, firstPage(limit ?? DEFAULT_PAGE_SIZE)), { keepFocus: true, noScroll: true });
	}

	function state(i: IntegrationSummary) {
		if (i.paused) return { tone: 'neutral', icon: 'pause', label: 'Paused' } as const;
		if (i.active_version == null)
			return { tone: 'warn', icon: 'alert', label: 'No active version' } as const;
		return { tone: 'ok', icon: 'closed', label: 'Live' } as const;
	}
</script>

<PageHeader title={section.label} summary={section.summary} />

<form class="filters" method="GET" action={base} {onsubmit} aria-label="Filter integrations">
	<div class="field grow">
		<label for="f-q">Search</label>
		<input
			id="f-q"
			name="q"
			type="search"
			value={filters.q ?? ''}
			maxlength="128"
			placeholder="id or description"
		/>
	</div>
	<div class="field">
		<label for="f-tenant">Owner tenant</label>
		{#if access.tenantScoped && access.tenants.length}
			<select id="f-tenant" name="tenant_id" value={filters.tenant_id ?? ''}>
				<option value="">All my tenants</option>
				{#each access.tenants as t (t)}<option value={t}>{t}</option>{/each}
			</select>
		{:else}
			<input
				id="f-tenant"
				name="tenant_id"
				value={filters.tenant_id ?? ''}
				maxlength="64"
				placeholder="orders-service"
				autocomplete="off"
			/>
		{/if}
	</div>
	<div class="field">
		<label for="f-capability">Capability</label>
		<input
			id="f-capability"
			name="capability"
			value={filters.capability ?? ''}
			maxlength="128"
			placeholder="events.track@v1"
			autocomplete="off"
		/>
	</div>
	<div class="field">
		<label for="f-paused">State</label>
		<select id="f-paused" name="paused" value={filters.paused ?? ''}>
			<option value="">Any</option>
			<option value="true">Paused only</option>
			<option value="false">Live only</option>
		</select>
	</div>
	{#if !access.tenantScoped}
		<div class="field">
			<label for="f-vendor">Vendor</label>
			<input
				id="f-vendor"
				name="vendor_id"
				value={filters.vendor_id ?? ''}
				maxlength="64"
				placeholder="clevertap"
				autocomplete="off"
			/>
		</div>
		<div class="field">
			<label for="f-account">Account</label>
			<input
				id="f-account"
				name="account_id"
				value={filters.account_id ?? ''}
				maxlength="64"
				placeholder="ct-brandA-in"
				autocomplete="off"
			/>
		</div>
	{/if}
	<div class="field">
		<label for="f-limit">Per page</label>
		<select id="f-limit" name="limit" value={String(paging.limit)}>
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
	noun="integrations"
	caption="Integrations"
	emptyTitle={filtered ? 'No integrations match these filters' : 'No integrations yet'}
	{filtered}
	clearHref={hrefFor({})}
	columns={[
		{ label: 'Integration' },
		{ label: 'Owner tenant' },
		{ label: 'Capability' },
		{ label: 'State' },
		{ label: 'Active version', numeric: true },
		{ label: 'Latest version', numeric: true },
		{ label: 'Delivery' },
		{ label: 'Targets' },
		{ label: 'Updated' }
	]}
	onretry={invalidateAll}
>
	{#snippet row(i)}
		{@const s = state(i)}
		<th scope="row" class:paused={i.paused}>
			<code class="id">{i.integration_id}</code>
			{#if i.description}<span class="desc">{i.description}</span>{/if}
		</th>
		<td>{i.owner_tenant}</td>
		<td><code>{i.capability}</code></td>
		<td><StatusBadge tone={s.tone} icon={s.icon} label={s.label} /></td>
		<td class="numeric">{i.active_version == null ? 'None' : `v${i.active_version}`}</td>
		<td class="numeric">
			{i.latest_version == null ? 'None' : `v${i.latest_version}`}{#if i.has_draft}<span
					class="draft">draft</span
				>{/if}
		</td>
		<td>{i.delivery_mode ?? '-'}</td>
		<td>
			{#each i.targets as t, n (n)}<span class="target"
					>{t.vendor_id}/{t.account_id} <span class="weight">{t.weight}%</span></span
				>{:else}-{/each}
		</td>
		<td>{formatUtc(i.updated_at)}</td>
	{/snippet}
	{#snippet empty()}
		{#if filtered}
			<p>Nothing on this page matches. Remove a filter or clear them all.</p>
		{:else}
			<p>Integrations appear here once a team creates one with hubctl or the admin API.</p>
		{/if}
	{/snippet}
	{#snippet footer(page)}
		<Pager
			{paging}
			nextCursor={page.next_cursor}
			count={page.items.length}
			noun="integrations"
			href={(p) => hrefFor(filters, p)}
		/>
	{/snippet}
</ListView>

<style>
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
	th code {
		display: block;
	}
	th.paused code {
		color: var(--ink-muted);
	}
	.desc {
		display: block;
		max-width: 22rem;
		overflow: hidden;
		text-overflow: ellipsis;
		color: var(--ink-muted);
		font-size: var(--text-xs);
		font-weight: 400;
	}
	.draft {
		margin-left: 0.35rem;
		padding: 0 0.3rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.target + .target::before {
		content: ', ';
	}
	.weight {
		color: var(--ink-muted);
	}
</style>

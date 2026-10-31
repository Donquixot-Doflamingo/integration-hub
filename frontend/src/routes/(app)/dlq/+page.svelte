<script lang="ts">
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import Icon from '$lib/components/Icon.svelte';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Pager from '$lib/components/Pager.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { sectionFor } from '$lib/nav';
	import { formatNumber, formatUtc } from '$lib/utils/format';
	import { DEFAULT_PAGE_SIZE, PAGE_SIZES, type Paging } from '$lib/utils/paging';
	import { lastRange, toLocalInput } from '$lib/utils/time';
	import type { PageData } from './$types';
	import {
		chipValue,
		COMMON_REASONS,
		FILTER_KEYS,
		filterLabels,
		filtersFromForm,
		isFiltered,
		replayedChoices,
		searchFor,
		without,
		type Filters
	} from './filters';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/dlq')!;

	const filters = $derived(data.state.filters);
	const paging = $derived(data.state.paging);
	const access = $derived(data.access);
	const scoped = $derived(access.tenantScoped);
	const filtered = $derived(isFiltered(filters));
	const base = resolve('/dlq');
	const firstPage = (limit: number): Paging => ({ cursor: undefined, prev: [], limit });
	const hrefFor = (f: Filters, p: Paging = firstPage(paging.limit)) => base + searchFor(f, p);
	const chips = $derived(FILTER_KEYS.filter((k) => !!filters[k]));
	const ranges = [
		{ label: 'Last hour', ms: 3_600_000 },
		{ label: 'Last 24 hours', ms: 86_400_000 },
		{ label: 'Last 14 days', ms: 14 * 86_400_000 }
	];

	function onsubmit(e: SubmitEvent) {
		// Without JS the browser submits the GET form as is; with JS, build a clean URL.
		e.preventDefault();
		const { filters: next, limit } = filtersFromForm(
			new FormData(e.currentTarget as HTMLFormElement)
		);
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- hrefFor builds on resolve()
		goto(hrefFor(next, firstPage(limit ?? DEFAULT_PAGE_SIZE)), { keepFocus: true, noScroll: true });
	}
</script>

<PageHeader
	title={section.label}
	summary="Dead-lettered requests, newest first. Kept 14 days. Filter them, open one to see why it failed."
/>

<form class="filters" method="GET" action={base} {onsubmit} aria-label="Filter dead letters">
	<div class="row">
		<div class="field">
			<label for="d-tenant">Tenant</label>
			{#if scoped && access.tenants.length}
				<select id="d-tenant" name="tenant_id" value={filters.tenant_id ?? ''}>
					<option value="">All my tenants</option>
					{#each access.tenants as t (t)}<option value={t}>{t}</option>{/each}
				</select>
			{:else}
				<input
					id="d-tenant"
					name="tenant_id"
					value={filters.tenant_id ?? ''}
					maxlength="64"
					placeholder="orders-service"
					autocomplete="off"
				/>
			{/if}
		</div>
		<div class="field">
			<label for="d-integration">Integration</label>
			<input
				id="d-integration"
				name="integration_id"
				value={filters.integration_id ?? ''}
				maxlength="64"
				placeholder="int_evt_brandA"
				autocomplete="off"
			/>
		</div>
		{#if !scoped}
			<div class="field">
				<label for="d-vendor">Vendor</label>
				<input
					id="d-vendor"
					name="vendor_id"
					value={filters.vendor_id ?? ''}
					maxlength="64"
					placeholder="clevertap"
					autocomplete="off"
				/>
			</div>
			<div class="field">
				<label for="d-account">Account</label>
				<input
					id="d-account"
					name="account_id"
					value={filters.account_id ?? ''}
					maxlength="64"
					placeholder="ct-brandA-in"
					autocomplete="off"
				/>
			</div>
		{/if}
		<div class="field">
			<label for="d-reason">Reason <span class="hint">(error code or poison)</span></label>
			<input
				id="d-reason"
				name="reason"
				value={filters.reason ?? ''}
				maxlength="64"
				placeholder="UPSTREAM_REJECTED"
				autocomplete="off"
				list="d-reasons"
			/>
			<datalist id="d-reasons">
				{#each COMMON_REASONS as r (r)}<option value={r}></option>{/each}
			</datalist>
		</div>
		<div class="field">
			<label for="d-replayed">Replayed</label>
			<select id="d-replayed" name="replayed" value={filters.replayed ?? ''}>
				<option value="">Any</option>
				<option value="false">{replayedChoices.false}</option>
				<option value="true">{replayedChoices.true}</option>
			</select>
		</div>
	</div>
	<div class="row">
		<div class="field">
			<label for="d-from">From (UTC)</label>
			<input
				id="d-from"
				name="from"
				type="datetime-local"
				value={toLocalInput(filters.from)}
				aria-invalid={data.state.problem ? 'true' : undefined}
				aria-describedby={data.state.problem ? 'd-range-problem' : 'd-range-hint'}
			/>
		</div>
		<div class="field">
			<label for="d-to">To (UTC)</label>
			<input
				id="d-to"
				name="to"
				type="datetime-local"
				value={toLocalInput(filters.to)}
				aria-invalid={data.state.problem ? 'true' : undefined}
				aria-describedby={data.state.problem ? 'd-range-problem' : 'd-range-hint'}
			/>
		</div>
		<p id="d-range-hint" class="hint range-hint">On the time the request was dead-lettered.</p>
		<ul class="ranges" aria-label="Quick time ranges">
			{#each ranges as r (r.label)}
				<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- hrefFor builds on resolve() -->
				<li><a href={hrefFor({ ...filters, ...lastRange(r.ms) })}>{r.label}</a></li>
			{/each}
		</ul>
		<div class="field">
			<label for="d-limit">Per page</label>
			<select id="d-limit" name="limit" value={String(paging.limit)}>
				{#each PAGE_SIZES as n (n)}<option value={String(n)}>{n}</option>{/each}
			</select>
		</div>
		<button type="submit" class="apply">Apply</button>
	</div>
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
						aria-label="Remove filter {filterLabels[key]}: {chipValue(key, filters)}"
					>
						<span class="chip-key">{filterLabels[key]}</span>
						{chipValue(key, filters)}
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

{#if data.state.problem}
	<p id="d-range-problem" class="problem" role="alert">
		<Icon name="alert" size={16} />{data.state.problem} Change the time range and apply again.
	</p>
{:else if data.list}
	<ListView
		result={data.list}
		noun="dead letters"
		caption="Dead-lettered requests, newest first"
		emptyTitle={filtered ? 'No dead letters match these filters' : 'The dead-letter queue is empty'}
		{filtered}
		clearHref={hrefFor({})}
		columns={[
			{ label: 'Request id' },
			{ label: 'Tenant' },
			{ label: 'Integration' },
			...(scoped ? [] : [{ label: 'Vendor / account' }]),
			{ label: 'Reason' },
			{ label: 'Attempts', numeric: true },
			{ label: 'Dead-lettered' },
			{ label: 'Replay' },
			{ label: 'Expires' }
		]}
		onretry={invalidateAll}
	>
		{#snippet row(d)}
			<th scope="row">
				<a href={resolve('/(app)/dlq/[request_id]', { request_id: d.request_id })}
					><code class="id">{d.request_id}</code></a
				>
			</th>
			<td>{d.tenant_id}</td>
			<td>
				<a
					href={resolve('/(app)/integrations/[integration_id]', {
						integration_id: d.integration_id
					})}><code>{d.integration_id}</code></a
				>
			</td>
			{#if !scoped}
				<td>{d.vendor_id ?? '-'}{d.account_id ? ` / ${d.account_id}` : ''}</td>
			{/if}
			<td class="reason">
				<code>{d.reason}</code>
				{#if d.message}<span class="message">{d.message}</span>{/if}
			</td>
			<td class="numeric">{formatNumber(d.attempts)}</td>
			<td>{formatUtc(d.dead_lettered_at)}</td>
			<td>
				{#if d.replayed_at}
					<StatusBadge tone="ok" icon="retry" label={`Replayed ${formatUtc(d.replayed_at)}`} />
				{:else}
					<StatusBadge tone="warn" icon="dlq" label="Waiting" />
				{/if}
			</td>
			<td>{formatUtc(d.expires_at)}</td>
		{/snippet}
		{#snippet empty()}
			{#if filtered}
				<p>Nothing matches. Widen the time range or remove a filter.</p>
			{:else}
				<p>Nothing is waiting for a replay. Requests land here after their final failed attempt.</p>
			{/if}
		{/snippet}
		{#snippet footer(page)}
			<Pager
				{paging}
				nextCursor={page.next_cursor}
				count={page.items.length}
				noun="dead letters"
				href={(p) => hrefFor(filters, p)}
			/>
		{/snippet}
	</ListView>
{/if}

<style>
	.filters {
		display: grid;
		gap: var(--space-3);
		margin-bottom: var(--space-3);
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: var(--space-3);
	}
	.field {
		display: grid;
		gap: 0.2rem;
		min-width: 9rem;
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
	[aria-invalid='true'] {
		border-color: var(--status-danger);
	}
	.hint {
		color: var(--ink-muted);
		font-size: var(--text-xs);
		font-weight: 400;
	}
	.range-hint {
		margin: 0 0 0.5rem;
	}
	.ranges {
		display: flex;
		gap: var(--space-2);
		margin: 0 0 0.45rem;
		padding: 0;
		list-style: none;
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
	.problem {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		padding: var(--space-3);
		border-left: 4px solid var(--status-danger);
		background: var(--status-danger-wash);
		max-width: 46rem;
	}
	.reason code {
		display: block;
	}
	.message {
		display: block;
		max-width: 24rem;
		overflow: hidden;
		text-overflow: ellipsis;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
</style>

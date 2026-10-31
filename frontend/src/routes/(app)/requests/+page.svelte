<script lang="ts">
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import Icon from '$lib/components/Icon.svelte';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Pager from '$lib/components/Pager.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { sectionFor } from '$lib/nav';
	import { requestStatus } from '$lib/status';
	import { formatDurationMs, formatNumber, formatUtc } from '$lib/utils/format';
	import { DEFAULT_PAGE_SIZE, PAGE_SIZES, type Paging } from '$lib/utils/paging';
	import type { PageData } from './$types';
	import {
		chipValue,
		FILTER_KEYS,
		filterLabels,
		filtersFromForm,
		isFiltered,
		lastRange,
		REQUEST_STATES,
		searchFor,
		toLocalInput,
		without,
		type Filters
	} from './filters';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/requests')!;

	const filters = $derived(data.state.filters);
	const paging = $derived(data.state.paging);
	const access = $derived(data.access);
	const filtered = $derived(isFiltered(filters));
	const base = resolve('/requests');
	const firstPage = (limit: number): Paging => ({ cursor: undefined, prev: [], limit });
	const hrefFor = (f: Filters, p: Paging = firstPage(paging.limit)) => base + searchFor(f, p);
	const chips = $derived(
		FILTER_KEYS.filter((k) => {
			const v = filters[k];
			return Array.isArray(v) ? v.length > 0 : !!v;
		})
	);
	const stateLabel = (s: keyof typeof requestStatus) => requestStatus[s].label;
	const ranges = [
		{ label: 'Last hour', ms: 3_600_000 },
		{ label: 'Last 24 hours', ms: 86_400_000 },
		{ label: 'Last 7 days', ms: 7 * 86_400_000 }
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
	summary="Search requests by tenant, integration, status, error code, time and idempotency key. Newest first."
/>

<form class="filters" method="GET" action={base} {onsubmit} aria-label="Search requests">
	<div class="row">
		<div class="field">
			<label for="q-tenant">Tenant</label>
			{#if access.tenantScoped && access.tenants.length}
				<select id="q-tenant" name="tenant_id" value={filters.tenant_id ?? ''}>
					<option value="">All my tenants</option>
					{#each access.tenants as t (t)}<option value={t}>{t}</option>{/each}
				</select>
			{:else}
				<input
					id="q-tenant"
					name="tenant_id"
					value={filters.tenant_id ?? ''}
					maxlength="64"
					placeholder="orders-service"
					autocomplete="off"
				/>
			{/if}
		</div>
		<div class="field">
			<label for="q-integration">Integration</label>
			<input
				id="q-integration"
				name="integration_id"
				value={filters.integration_id ?? ''}
				maxlength="64"
				placeholder="int_evt_brandA"
				autocomplete="off"
			/>
		</div>
		<div class="field">
			<label for="q-code">Error codes <span class="hint">(comma-separated, any of)</span></label>
			<input
				id="q-code"
				name="code"
				value={(filters.code ?? []).join(', ')}
				maxlength="256"
				placeholder="UPSTREAM_TIMEOUT"
				autocomplete="off"
			/>
		</div>
		<div class="field grow">
			<label for="q-idem">Idempotency key</label>
			<input
				id="q-idem"
				name="idempotency_key"
				value={filters.idempotency_key ?? ''}
				maxlength="256"
				placeholder="order-8821-created"
				autocomplete="off"
			/>
		</div>
		{#if !access.tenantScoped}
			<div class="field">
				<label for="q-vendor">Vendor</label>
				<input
					id="q-vendor"
					name="vendor_id"
					value={filters.vendor_id ?? ''}
					maxlength="64"
					placeholder="clevertap"
					autocomplete="off"
				/>
			</div>
			<div class="field">
				<label for="q-account">Account</label>
				<input
					id="q-account"
					name="account_id"
					value={filters.account_id ?? ''}
					maxlength="64"
					placeholder="ct-brandA-in"
					autocomplete="off"
				/>
			</div>
		{/if}
	</div>
	<div class="row">
		<fieldset class="states">
			<legend>Status <span class="hint">(any of)</span></legend>
			{#each REQUEST_STATES as s (s)}
				<label class="check">
					<input
						type="checkbox"
						name="status"
						value={s}
						checked={filters.status?.includes(s) ?? false}
					/>
					{requestStatus[s].label}
				</label>
			{/each}
		</fieldset>
	</div>
	<div class="row">
		<div class="field">
			<label for="q-from">From (UTC)</label>
			<input
				id="q-from"
				name="from"
				type="datetime-local"
				value={toLocalInput(filters.from)}
				aria-invalid={data.state.problem ? 'true' : undefined}
				aria-describedby={data.state.problem ? 'q-range-problem' : 'q-range-hint'}
			/>
		</div>
		<div class="field">
			<label for="q-to">To (UTC)</label>
			<input
				id="q-to"
				name="to"
				type="datetime-local"
				value={toLocalInput(filters.to)}
				aria-invalid={data.state.problem ? 'true' : undefined}
				aria-describedby={data.state.problem ? 'q-range-problem' : 'q-range-hint'}
			/>
		</div>
		<p id="q-range-hint" class="hint range-hint">
			At most 31 days, on the request's creation time.
		</p>
		<ul class="ranges" aria-label="Quick time ranges">
			{#each ranges as r (r.label)}
				<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- hrefFor builds on resolve() -->
				<li><a href={hrefFor({ ...filters, ...lastRange(r.ms) })}>{r.label}</a></li>
			{/each}
		</ul>
		<div class="field">
			<label for="q-limit">Per page</label>
			<select id="q-limit" name="limit" value={String(paging.limit)}>
				{#each PAGE_SIZES as n (n)}<option value={String(n)}>{n}</option>{/each}
			</select>
		</div>
		<button type="submit" class="apply">Search</button>
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
						aria-label="Remove filter {filterLabels[key]}: {chipValue(key, filters, stateLabel)}"
					>
						<span class="chip-key">{filterLabels[key]}</span>
						{chipValue(key, filters, stateLabel)}
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
	<p id="q-range-problem" class="problem" role="alert">
		<Icon name="alert" size={16} />{data.state.problem} Change the time range and search again.
	</p>
{:else if data.list}
	<ListView
		result={data.list}
		noun="requests"
		caption="Requests, newest first"
		emptyTitle={filtered ? 'No requests match this search' : 'No requests yet'}
		{filtered}
		clearHref={hrefFor({})}
		columns={[
			{ label: 'Request id' },
			{ label: 'Status' },
			{ label: 'Tenant' },
			{ label: 'Integration' },
			{ label: 'Version', numeric: true },
			...(access.tenantScoped ? [] : [{ label: 'Vendor / account' }]),
			{ label: 'Mode' },
			{ label: 'Attempts', numeric: true },
			{ label: 'Duration', numeric: true },
			{ label: 'Created' }
		]}
		onretry={invalidateAll}
	>
		{#snippet row(r)}
			{@const s = requestStatus[r.status]}
			<th scope="row">
				<a href={resolve('/(app)/requests/[request_id]', { request_id: r.request_id })}
					><code class="id">{r.request_id}</code></a
				>
			</th>
			<td>
				<StatusBadge tone={s.tone} icon={s.icon} label={s.label} />
				{#if r.code}<code class="code">{r.code}</code>{/if}
			</td>
			<td>{r.tenant_id}</td>
			<td>
				<a
					href={resolve('/(app)/integrations/[integration_id]', {
						integration_id: r.integration_id
					})}><code>{r.integration_id}</code></a
				>
			</td>
			<td class="numeric">{r.final_version == null ? '-' : `v${r.final_version}`}</td>
			{#if !access.tenantScoped}
				<td>{r.vendor_id ?? '-'}{r.account_id ? ` / ${r.account_id}` : ''}</td>
			{/if}
			<td>{r.mode}</td>
			<td class="numeric">{formatNumber(r.attempts)}</td>
			<td class="numeric">{formatDurationMs(r.duration_ms)}</td>
			<td>{formatUtc(r.created_at)}</td>
		{/snippet}
		{#snippet empty()}
			{#if filtered}
				<p>Nothing in this range matches. Widen the time range or remove a filter.</p>
			{:else}
				<p>Requests show up here as soon as callers invoke an integration.</p>
			{/if}
		{/snippet}
		{#snippet footer(page)}
			<Pager
				{paging}
				nextCursor={page.next_cursor}
				count={page.items.length}
				noun="requests"
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
	.field.grow {
		flex: 1 1 14rem;
	}
	label,
	legend {
		font-size: var(--text-xs);
		font-weight: 600;
		color: var(--ink-muted);
	}
	input:not([type='checkbox']),
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
	.states {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1) var(--space-3);
		margin: 0;
		padding: 0;
		border: 0;
	}
	.states legend {
		margin-bottom: var(--space-1);
	}
	.check {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		color: var(--ink);
		font-size: var(--text-sm);
		font-weight: 400;
	}
	.check input {
		accent-color: var(--signal);
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
	.code {
		margin-left: var(--space-2);
		font-size: var(--text-xs);
	}
</style>

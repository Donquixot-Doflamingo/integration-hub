<script lang="ts">
	import { tick } from 'svelte';
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import FieldError from '$lib/components/FieldError.svelte';
	import FormProblems from '$lib/components/FormProblems.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Pager from '$lib/components/Pager.svelte';
	import { sectionFor } from '$lib/nav';
	import { formatUtc } from '$lib/utils/format';
	import type { FilterProblems } from '$lib/utils/filter-problems';
	import { DEFAULT_PAGE_SIZE, PAGE_SIZES, type Paging } from '$lib/utils/paging';
	import { lastRange, toLocalInput } from '$lib/utils/time';
	import type { PageData } from './$types';
	import {
		chipValue,
		COMMON_ACTIONS,
		FILTER_KEYS,
		filterLabels,
		filtersFromForm,
		isFiltered,
		OBJECT_TYPES,
		objectTypeLabels,
		searchFor,
		without,
		type Filters,
		type FilterKey
	} from './filters';
	import { objectHref } from '$lib/objects';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/change-log')!;

	const filters = $derived(data.state.filters);
	const paging = $derived(data.state.paging);
	const filtered = $derived(isFiltered(filters));
	const base = resolve('/change-log');
	const firstPage = (limit: number): Paging => ({ cursor: undefined, prev: [], limit });
	const hrefFor = (f: Filters, p: Paging = firstPage(paging.limit)) => base + searchFor(f, p);
	const chips = $derived(FILTER_KEYS.filter((k) => !!filters[k]));
	const ranges = [
		{ label: 'Last hour', ms: 3_600_000 },
		{ label: 'Last 24 hours', ms: 86_400_000 },
		{ label: 'Last 7 days', ms: 7 * 86_400_000 }
	];

	// Filters whose text does not fit its rule: marked, and the search does not run (HUB-455).
	let problems = $state.raw<FilterProblems<FilterKey>>({});
	const problemItems = $derived(
		(Object.entries(problems) as [FilterKey, string][]).map(([k, message]) => ({
			label: filterLabels[k],
			message
		}))
	);
	// New URL state (a search, a chip removed): the marks belonged to the old form text.
	$effect(() => {
		void data.state;
		problems = {};
	});

	function onsubmit(e: SubmitEvent) {
		e.preventDefault();
		const form = e.currentTarget as HTMLFormElement;
		const { filters: next, limit, problems: found } = filtersFromForm(new FormData(form));
		problems = found;
		// A value that does not fit is not searched without: mark it and stay (HUB-455).
		if (Object.keys(found).length) {
			tick().then(() =>
				form
					.querySelector<HTMLElement>('[aria-invalid="true"][aria-describedby$="-error"]')
					?.focus()
			);
			return;
		}
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- hrefFor builds on resolve()
		goto(hrefFor(next, firstPage(limit ?? DEFAULT_PAGE_SIZE)), { keepFocus: true, noScroll: true });
	}
</script>

<PageHeader title={section.label} summary={section.summary} />

<FormProblems title="Fix these filters before searching" items={problemItems} />

<form class="filters" method="GET" action={base} {onsubmit} aria-label="Filter the change log">
	<div class="row">
		<div class="field">
			<label for="cl-actor">Actor</label>
			<input
				id="cl-actor"
				name="actor"
				aria-invalid={problems.actor ? 'true' : undefined}
				aria-describedby={problems.actor ? 'cl-actor-error' : undefined}
				value={filters.actor ?? ''}
				maxlength="256"
				placeholder="priya@example.com"
				autocomplete="off"
			/>
			<FieldError id="cl-actor-error" message={problems.actor} />
		</div>
		<div class="field">
			<label for="cl-action">Action <span class="hint">(operation)</span></label>
			<input
				id="cl-action"
				name="action"
				aria-invalid={problems.action ? 'true' : undefined}
				aria-describedby={problems.action ? 'cl-action-error' : undefined}
				value={filters.action ?? ''}
				maxlength="64"
				placeholder="publishIntegrationVersion"
				autocomplete="off"
				list="cl-actions"
			/>
			<FieldError id="cl-action-error" message={problems.action} />
			<datalist id="cl-actions">
				{#each COMMON_ACTIONS as a (a)}<option value={a}></option>{/each}
			</datalist>
		</div>
		<div class="field">
			<label for="cl-type">Object type</label>
			<select id="cl-type" name="object_type" value={filters.object_type ?? ''}>
				<option value="">Any</option>
				{#each OBJECT_TYPES as t (t)}<option value={t}>{objectTypeLabels[t]}</option>{/each}
			</select>
		</div>
		<div class="field">
			<label for="cl-object">Object id</label>
			<input
				id="cl-object"
				name="object_id"
				aria-invalid={problems.object_id ? 'true' : undefined}
				aria-describedby={problems.object_id ? 'cl-object-error' : undefined}
				value={filters.object_id ?? ''}
				maxlength="128"
				placeholder="int_evt_brandA"
				autocomplete="off"
			/>
			<FieldError id="cl-object-error" message={problems.object_id} />
		</div>
	</div>
	<div class="row">
		<div class="field">
			<label for="cl-from">From (UTC)</label>
			<input
				id="cl-from"
				name="from"
				type="datetime-local"
				step="1"
				value={toLocalInput(filters.from)}
				aria-invalid={data.state.problem ? 'true' : undefined}
				aria-describedby={data.state.problem ? 'cl-range-problem' : undefined}
			/>
		</div>
		<div class="field">
			<label for="cl-to">To (UTC)</label>
			<input
				id="cl-to"
				name="to"
				type="datetime-local"
				step="1"
				value={toLocalInput(filters.to)}
				aria-invalid={data.state.problem ? 'true' : undefined}
				aria-describedby={data.state.problem ? 'cl-range-problem' : undefined}
			/>
		</div>
		<ul class="ranges" aria-label="Quick time ranges">
			{#each ranges as r (r.label)}
				<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- hrefFor builds on resolve() -->
				<li><a href={hrefFor({ ...filters, ...lastRange(r.ms) })}>{r.label}</a></li>
			{/each}
		</ul>
		<div class="field">
			<label for="cl-limit">Per page</label>
			<select id="cl-limit" name="limit" value={String(paging.limit)}>
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
	<p id="cl-range-problem" class="problem" role="alert">
		<Icon name="alert" size={16} />{data.state.problem} Change the time range and apply again.
	</p>
{:else if data.list}
	<ListView
		result={data.list}
		noun="change log entries"
		caption="Change log, newest first"
		emptyTitle={filtered ? 'No changes match these filters' : 'No changes recorded yet'}
		{filtered}
		clearHref={hrefFor({})}
		columns={[
			{ label: 'Change' },
			{ label: 'When' },
			{ label: 'Actor' },
			{ label: 'Action' },
			{ label: 'Object' },
			{ label: 'Reason' },
			{ label: 'Snapshot', numeric: true }
		]}
		onretry={invalidateAll}
	>
		{#snippet row(c)}
			{@const object = objectHref(c.object_type, c.object_id)}
			<th scope="row">
				<a href={resolve('/(app)/change-log/[change_id]', { change_id: c.change_id })}
					><code class="id">{c.change_id}</code></a
				>
			</th>
			<td>{formatUtc(c.at)}</td>
			<td>
				{c.actor}
				{#if c.actor_roles?.length}<span class="roles">({c.actor_roles.join(', ')})</span>{/if}
			</td>
			<td><code>{c.action}</code></td>
			<td>
				<span class="type">{objectTypeLabels[c.object_type]}</span>
				<!-- eslint-disable svelte/no-navigation-without-resolve -- objectHref() builds on resolve() -->
				{#if object}<a href={object}><code class="id">{c.object_id}</code></a>{:else}<code
						class="id">{c.object_id}</code
					>{/if}
				<!-- eslint-enable svelte/no-navigation-without-resolve -->
			</td>
			<td class="reason" title={c.reason}>{c.reason}</td>
			<td class="numeric">{c.snapshot_version ?? '-'}</td>
		{/snippet}
		{#snippet empty()}
			{#if filtered}
				<p>Nothing matches. Widen the time range or remove a filter.</p>
			{:else}
				<p>Every mutation made through the admin API is recorded here with its reason.</p>
			{/if}
		{/snippet}
		{#snippet footer(page)}
			<Pager
				{paging}
				nextCursor={page.next_cursor}
				count={page.items.length}
				noun="change log entries"
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
	.roles,
	.type {
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.type {
		margin-right: var(--space-1);
	}
	.reason {
		max-width: 24rem;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>

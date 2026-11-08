<script lang="ts">
	import type { SubmitFunction } from '@sveltejs/kit';
	import { applyAction, enhance } from '$app/forms';
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { fromPlainError, type ActionFailureData, type ActionSuccessData } from '$lib/api/actions';
	import { HubApiError } from '$lib/api/errors';
	import type { DlqBulkResult } from '$lib/api/types';
	import ChangeNotice from '$lib/components/ChangeNotice.svelte';
	import ErrorPanel from '$lib/components/ErrorPanel.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Pager from '$lib/components/Pager.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { sectionFor } from '$lib/nav';
	import { formatNumber, formatUtc } from '$lib/utils/format';
	import { DEFAULT_PAGE_SIZE, PAGE_SIZES, type Paging } from '$lib/utils/paging';
	import { lastRange, toLocalInput } from '$lib/utils/time';
	import type { PageData } from './$types';
	import BulkDialog from './BulkDialog.svelte';
	import { bulkDone, type BulkKind } from './bulk';
	import {
		chipValue,
		COMMON_REASONS,
		FILTER_KEYS,
		filterFields,
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

	// HUB-267: replay / purge by the current filters. Step 1 counts (dry run), step 2 confirms.
	let counting = $state<BulkKind | null>(null);
	let countError = $state.raw<{ kind: BulkKind; error: HubApiError } | null>(null);
	let nothing = $state<BulkKind | null>(null);
	let dialogOpen = $state(false);
	let dialog = $state.raw<{ kind: BulkKind; count: DlqBulkResult; filters: Filters } | null>(null);
	let notice = $state.raw<{
		message: string;
		changeId: string | null;
		requestId: string | null;
		link?: { href: string; label: string };
	} | null>(null);
	const verbs: Record<BulkKind, string> = { replay: 'replay', purge: 'purge' };

	function countWith(kind: BulkKind): SubmitFunction {
		return ({ cancel }) => {
			// The buttons stay focusable while counting (aria-disabled), so guard double submits here.
			if (counting) return cancel();
			counting = kind;
			countError = null;
			nothing = null;
			// The filters the count was made for (the dialog must not pick up later URL changes).
			const counted = { ...filters };
			return async ({ result }) => {
				try {
					if (result.type === 'success') {
						const count = (result.data as ActionSuccessData<DlqBulkResult>).result;
						if (count.matched === 0) {
							nothing = kind;
						} else {
							dialog = { kind, count, filters: counted };
							dialogOpen = true;
						}
					} else if (result.type === 'failure') {
						const d = result.data as ActionFailureData | undefined;
						countError = d?.error
							? { kind, error: fromPlainError(d.error) }
							: {
									kind,
									error: new HubApiError({
										status: result.status,
										code: 'VALIDATION_FAILED',
										message: 'The filters could not be counted.'
									})
								};
					} else if (result.type === 'redirect') {
						// The session expired: the action answered with the sign-in redirect (HUB-455).
						await applyAction(result);
					} else {
						countError = {
							kind,
							error: new HubApiError({
								status: result.type === 'error' ? (result.status ?? 500) : 500,
								code: 'UNEXPECTED_RESPONSE',
								message: 'The console could not count the matching dead letters. Try again.'
							})
						};
					}
				} finally {
					counting = null;
				}
			};
		};
	}

	function onBulkDone(d: ActionSuccessData) {
		if (!dialog) return;
		const r = d.result as DlqBulkResult;
		notice = {
			message: bulkDone(dialog.kind, r),
			changeId: d.changeId,
			requestId: d.requestId,
			link:
				dialog.kind === 'replay'
					? {
							href: hrefFor({ ...dialog.filters, replayed: 'true' }),
							label: 'Show the replayed entries'
						}
					: undefined
		};
	}

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

{#if notice}
	<ChangeNotice {...notice} ondismiss={() => (notice = null)} />
{/if}

{#if !data.state.problem}
	<!-- Replay: replayDeadLetters (editor); purge: purgeDeadLetters (admin), both from the contract. -->
	<RoleGate access={data.access} op="replayDeadLetters">
		<section class="bulk" aria-labelledby="bulk-heading">
			<h2 id="bulk-heading">Act on every match</h2>
			<p class="hint">
				Counts the dead letters these filters match on all pages (not only this one), then asks you
				to confirm with a reason.
			</p>
			<div class="bulk-actions">
				<form method="POST" action="?/countReplay" use:enhance={countWith('replay')}>
					{#each filterFields(filters) as [name, value] (name)}
						<input type="hidden" {name} {value} />
					{/each}
					<button type="submit" aria-disabled={counting ? 'true' : undefined}>
						<Icon name="retry" size={16} />{counting === 'replay'
							? 'Counting...'
							: 'Replay matching...'}
					</button>
				</form>
				<RoleGate access={data.access} op="purgeDeadLetters">
					<form
						method="POST"
						action="?/countPurge"
						use:enhance={countWith('purge')}
						class="danger-zone"
					>
						{#each filterFields(filters) as [name, value] (name)}
							<input type="hidden" {name} {value} />
						{/each}
						<button type="submit" class="danger" aria-disabled={counting ? 'true' : undefined}>
							<Icon name="close" size={16} />{counting === 'purge'
								? 'Counting...'
								: 'Purge matching...'}
						</button>
					</form>
				</RoleGate>
			</div>
			{#if counting}<p class="visually-hidden" role="status">Counting matching dead letters</p>{/if}
			{#if nothing}
				<p class="nothing" role="status">
					No dead letters match these filters, so there is nothing to {verbs[nothing]}.
				</p>
			{/if}
			{#if countError}
				<ErrorPanel
					error={countError.error}
					context="Could not count the dead letters to {verbs[countError.kind]}"
					headingLevel={3}
				/>
			{/if}
		</section>
	</RoleGate>
{/if}

{#if dialog}
	<BulkDialog
		bind:open={dialogOpen}
		kind={dialog.kind}
		count={dialog.count}
		filters={dialog.filters}
		onsuccess={onBulkDone}
	/>
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
	.bulk {
		display: grid;
		gap: var(--space-2);
		margin-bottom: var(--space-3);
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
		max-width: 72rem;
	}
	.bulk h2 {
		font-size: var(--text-md);
	}
	.bulk .hint {
		margin: 0;
		font-size: var(--text-sm);
	}
	.bulk-actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}
	/* Purge sits apart from replay (admin-console-ux §8: destructive actions separated). */
	.danger-zone {
		margin-left: auto;
		padding-left: var(--space-3);
		border-left: 1px solid var(--rule);
	}
	.bulk button {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		height: 2.125rem;
		padding: 0 0.9rem;
		border: 1px solid var(--signal);
		border-radius: var(--radius-sm);
		background: var(--surface);
		color: var(--signal);
		font-weight: 700;
		cursor: pointer;
	}
	.bulk button.danger {
		border-color: var(--status-danger);
		color: var(--status-danger);
	}
	/* Not `disabled` while counting: a disabled button drops focus, and the dialog returns focus to it. */
	.bulk button[aria-disabled='true'] {
		opacity: 0.55;
		cursor: not-allowed;
	}
	.nothing {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-sm);
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

<script lang="ts">
	import { invalidate, invalidateAll } from '$app/navigation';
	import type { ApiResult } from '$lib/api/client';
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { HubApiError } from '$lib/api/errors';
	import type { Breaker, BreakerChange } from '$lib/api/types';
	import ChangeNotice from '$lib/components/ChangeNotice.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { sectionFor } from '$lib/nav';
	import { breakerStatus } from '$lib/status';
	import {
		formatAgo,
		formatDurationMs,
		formatNumber,
		formatRatio,
		formatUtc
	} from '$lib/utils/format';
	import type { PageData } from './$types';
	import type { BoardData } from './+page';
	import ForceDialog from './ForceDialog.svelte';
	import type { ForceKind } from './force';
	import {
		buildBoard,
		DEFAULT_REFRESH_S,
		forcedLabel,
		HEALTH_DEPENDENCY,
		lifecycleStatus,
		REFRESH_CHOICES,
		retryLabel,
		summarizeBoard
	} from './board';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/health')!;

	// Auto-refresh (HUB-261): configurable interval, pausable, skipped while the tab is hidden.
	let intervalS = $state<number>(DEFAULT_REFRESH_S);
	let paused = $state(false);
	let refreshing = $state(false);
	let now = $state(Date.now());

	async function refresh() {
		if (refreshing) return;
		refreshing = true;
		try {
			await invalidate(HEALTH_DEPENDENCY);
		} finally {
			refreshing = false;
		}
	}

	$effect(() => {
		const t = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(t);
	});

	// HUB-262: breaker force (admin only). The board holds still while a dialog is open.
	let forceOpen = $state(false);
	let forceKind = $state<ForceKind>('open');
	let forceTarget = $state.raw<Breaker | null>(null);
	let notice = $state.raw<{ message: string; changeId: string | null } | null>(null);
	function openForce(kind: ForceKind, b: Breaker) {
		forceKind = kind;
		forceTarget = b;
		forceOpen = true;
	}
	function onForced(d: ActionSuccessData) {
		const b = (d.result as BreakerChange).breaker;
		const name = `${b.account_id} ${b.endpoint_ref}`;
		const state = breakerStatus[b.state].label.toLowerCase();
		const held = forcedLabel(b);
		notice = {
			message: held
				? `${held}: ${name} is now ${state}.`
				: `Force cleared: ${name} is back under automatic control (${state}).`,
			changeId: d.changeId
		};
	}

	$effect(() => {
		if (paused || forceOpen) return;
		const t = setInterval(() => {
			if (document.visibilityState !== 'hidden') refresh();
		}, intervalS * 1000);
		return () => clearInterval(t);
	});

	// A failed refresh keeps the last good board on screen, marked stale (admin-console-ux §6).
	type Shown = BoardData & { stale: HubApiError | null };
	let lastGood: Extract<ApiResult<Shown>, { ok: true }> | null = null;
	const result = $derived(
		data.board.then((r): ApiResult<Shown> => {
			if (r.ok) {
				lastGood = { ...r, data: { ...r.data, stale: null } };
				return lastGood;
			}
			return lastGood ? { ...lastGood, data: { ...lastGood.data, stale: r.error } } : r;
		})
	);

	const columns = $derived([
		{ label: 'Account endpoint' },
		{ label: 'State' },
		{ label: 'Failure rate', numeric: true },
		{ label: 'Calls in window', numeric: true },
		{ label: 'Parked', numeric: true },
		{ label: 'Oldest parked', numeric: true },
		{ label: 'Account lifecycle' },
		{ label: 'Updated' },
		...(data.access.isAdmin ? [{ label: 'Force' }] : [])
	]);
</script>

<PageHeader title={section.label} summary={section.summary} />

{#if notice}
	<ChangeNotice {...notice} ondismiss={() => (notice = null)} />
{/if}

{#if data.access.tenantScoped && data.access.known}
	<p class="note" role="note">
		The health board shows vendors, accounts and endpoints, which tenant viewers cannot see. Ask the
		platform team if a vendor looks unhealthy.
	</p>
{:else}
	<AsyncView {result} noun="the health board" onretry={invalidateAll} quiet>
		{#snippet children(b)}
			{@const groups = buildBoard(b.breakers, b.accounts)}
			{@const sum = summarizeBoard(b.breakers)}
			<div class="toolbar">
				<p class="summary" role="status">
					<strong>{sum.open} open</strong>, {sum.halfOpen} half-open, {sum.closed} closed{sum.forced
						? `, ${sum.forced} forced`
						: ''}. {formatNumber(sum.parked)} requests parked.
				</p>
				<div class="refresh" role="group" aria-label="Auto-refresh">
					<label for="refresh-every">Refresh every</label>
					<select id="refresh-every" bind:value={intervalS} disabled={paused}>
						{#each REFRESH_CHOICES as s (s)}<option value={s}>{s} s</option>{/each}
					</select>
					<button type="button" onclick={() => (paused = !paused)}>
						<Icon name={paused ? 'retry' : 'pause'} size={16} />{paused
							? 'Resume auto-refresh'
							: 'Pause auto-refresh'}
					</button>
					<button type="button" onclick={refresh} disabled={refreshing}>Refresh now</button>
					<span class="updated" title={formatUtc(new Date(b.fetchedAt).toISOString())}>
						{paused ? 'Paused. ' : ''}Updated {formatAgo(now - b.fetchedAt)}
					</span>
				</div>
			</div>

			{#if b.stale}
				<p class="stale" role="alert">
					<Icon name="alert" size={16} />
					<span>
						Stale: the last refresh failed with <code>{b.stale.code}</code>{b.stale.requestId
							? ` (request ${b.stale.requestId})`
							: ''}. Showing the board from {formatAgo(now - b.fetchedAt)}.
					</span>
				</p>
			{/if}
			{#if b.accountsError}
				<p class="stale" role="note">
					<Icon name="alert" size={16} />
					<span>
						Account lifecycle states are missing: listing accounts failed with
						<code>{b.accountsError.code}</code>.
					</span>
				</p>
			{/if}

			{#if groups.length === 0}
				<EmptyState title="No breakers reported">
					<p>Breakers appear once accounts have handled traffic or one is forced.</p>
				</EmptyState>
			{:else}
				<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
				<div class="table-scroll" role="region" aria-label="Breakers by vendor" tabindex="0">
					<table>
						<caption class="visually-hidden"
							>Circuit breakers grouped by vendor, open ones first</caption
						>
						<thead>
							<tr>
								{#each columns as c (c.label)}
									<th scope="col" class:numeric={c.numeric}>{c.label}</th>
								{/each}
							</tr>
						</thead>
						{#each groups as g (g.vendorId)}
							<tbody>
								<tr class="group">
									<th scope="rowgroup" colspan={columns.length}>
										{g.vendorId}
										<span class="muted"
											>{g.rows.length}
											{g.rows.length === 1 ? 'endpoint' : 'endpoints'}</span
										>
									</th>
								</tr>
								{#each g.rows as r (`${r.breaker.account_id}/${r.breaker.endpoint_ref}`)}
									{@const x = r.breaker}
									{@const s = breakerStatus[x.state]}
									{@const retry = retryLabel(x, now)}
									{@const forced = forcedLabel(x)}
									<tr class={x.state}>
										<th scope="row">
											<code class="id">{x.account_id}</code>
											<code class="ep">{x.endpoint_ref}</code>
										</th>
										<td>
											<StatusBadge tone={s.tone} icon={s.icon} label={s.label} />
											{#if retry}<span class="sub">{retry}</span>{/if}
											{#if forced}<span class="sub forced">{forced}</span>{/if}
											{#if x.state === 'open' && x.opened_at}<span
													class="sub"
													title={formatUtc(x.opened_at)}
													>open since {formatAgo(now - Date.parse(x.opened_at))}</span
												>{/if}
										</td>
										<td class="numeric">{formatRatio(x.failure_ratio)}</td>
										<td class="numeric">{formatNumber(x.window_calls)}</td>
										<td class="numeric">{formatNumber(x.parked_count)}</td>
										<td class="numeric">{formatDurationMs(x.parked_oldest_age_ms)}</td>
										<td>
											{#if r.lifecycle}
												{@const l = lifecycleStatus[r.lifecycle]}
												<StatusBadge tone={l.tone} icon={l.icon} label={l.label} />
											{:else}<span class="muted">Unknown</span>{/if}
										</td>
										<td title={formatUtc(x.updated_at)}
											>{formatAgo(now - Date.parse(x.updated_at))}</td
										>
										{#if data.access.isAdmin}
											<td>
												<RoleGate access={data.access} min="admin">
													<div
														class="force"
														role="group"
														aria-label="Force {x.account_id} {x.endpoint_ref}"
													>
														{#if x.forced !== 'open'}
															<button type="button" onclick={() => openForce('open', x)}
																>Force open</button
															>
														{/if}
														{#if x.forced !== 'closed'}
															<button type="button" onclick={() => openForce('close', x)}
																>Force closed</button
															>
														{/if}
														{#if x.forced && x.forced !== 'none'}
															<button type="button" onclick={() => openForce('clear', x)}
																>Clear force</button
															>
														{/if}
													</div>
												</RoleGate>
											</td>
										{/if}
									</tr>
								{/each}
							</tbody>
						{/each}
					</table>
				</div>
			{/if}
		{/snippet}
	</AsyncView>
{/if}

{#if forceTarget}
	<ForceDialog bind:open={forceOpen} kind={forceKind} breaker={forceTarget} onsuccess={onForced} />
{/if}

<style>
	.force {
		display: flex;
		gap: var(--space-1);
	}
	.force button {
		padding: 0.1rem 0.5rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-xs);
		font-weight: 700;
		cursor: pointer;
	}
	.force button:hover {
		border-color: var(--signal);
	}
	.note {
		color: var(--ink-muted);
		max-width: 46rem;
	}
	.toolbar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
		margin-bottom: var(--space-3);
	}
	.summary {
		margin: 0;
		font-size: var(--text-sm);
	}
	.refresh {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		font-size: var(--text-sm);
	}
	.refresh label {
		color: var(--ink-muted);
		font-weight: 600;
		font-size: var(--text-xs);
	}
	.refresh select,
	.refresh button {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		height: 2rem;
		padding: 0 0.6rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-sm);
		font-weight: 600;
		cursor: pointer;
	}
	.refresh button:disabled,
	.refresh select:disabled {
		opacity: 0.55;
		cursor: not-allowed;
	}
	.updated {
		color: var(--ink-muted);
		font-variant-numeric: tabular-nums;
		min-width: 9rem;
	}
	.stale {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		margin: 0 0 var(--space-3);
		padding: var(--space-2) var(--space-3);
		border-left: 4px solid var(--status-warn);
		background: var(--status-warn-wash);
		font-size: var(--text-sm);
		max-width: 72rem;
	}
	.table-scroll {
		overflow-x: auto;
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: var(--text-sm);
		font-variant-numeric: tabular-nums;
	}
	th,
	td {
		height: var(--row-height);
		padding: 0.2rem var(--space-3);
		text-align: left;
		white-space: nowrap;
		border-bottom: 1px solid var(--rule);
		background: var(--surface);
	}
	thead th {
		background: var(--surface-sunken);
		color: var(--ink-muted);
		font-weight: 600;
		font-size: var(--text-xs);
	}
	.numeric {
		text-align: right;
	}
	tr.group th {
		background: var(--surface-sunken);
		font-size: var(--text-sm);
	}
	tr.group .muted {
		margin-left: var(--space-2);
		font-weight: 400;
		font-size: var(--text-xs);
	}
	tbody th[scope='row'] {
		position: sticky;
		left: 0;
		z-index: 1;
		font-weight: 600;
		border-left: 4px solid transparent;
	}
	/* Open rows: thick left rule and bold id, on top of the "Open" text badge (not colour alone). */
	tr.open th[scope='row'] {
		border-left-color: var(--status-danger);
	}
	tr.half_open th[scope='row'] {
		border-left-color: var(--status-warn);
		border-left-style: dashed;
	}
	tr.open .id {
		font-weight: 800;
	}
	.ep {
		margin-left: var(--space-2);
		color: var(--ink-muted);
	}
	.sub {
		margin-left: var(--space-2);
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.forced {
		color: var(--ink);
		font-weight: 700;
	}
</style>

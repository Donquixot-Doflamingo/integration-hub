<script lang="ts">
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { AccountState } from '$lib/api/types';
	import Icon from '$lib/components/Icon.svelte';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Pager from '$lib/components/Pager.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { sectionFor } from '$lib/nav';
	import { formatNumber, formatUtc } from '$lib/utils/format';
	import { DEFAULT_PAGE_SIZE, PAGE_SIZES, type Paging } from '$lib/utils/paging';
	import type { PageData } from './$types';
	import { Q_MAX, searchFor, STATES, stateLabels, type Filters } from './filters';

	/** Accounts (HUB-291): vendor / state / search filters in the URL, paging, "Create account". */
	let { data }: { data: PageData } = $props();
	const section = sectionFor('/accounts')!;

	const filters = $derived(data.state.filters);
	const paging = $derived(data.state.paging);
	const filtered = $derived(!!(filters.q || filters.vendor_id || filters.state));
	const base = resolve('/accounts');
	const firstPage = (limit: number): Paging => ({ cursor: undefined, prev: [], limit });
	const hrefFor = (f: Filters, p: Paging = firstPage(paging.limit)) => base + searchFor(f, p);
	const accountHref = (id: string) => resolve('/(app)/accounts/[account_id]', { account_id: id });
	const newHref = $derived(
		resolve('/(app)/accounts/new') +
			(filters.vendor_id ? `?vendor_id=${encodeURIComponent(filters.vendor_id)}` : '')
	);

	const tone: Record<AccountState, 'ok' | 'warn' | 'neutral'> = {
		active: 'ok',
		draining: 'warn',
		retired: 'neutral'
	};
	const icon: Record<AccountState, 'closed' | 'halfOpen' | 'pause'> = {
		active: 'closed',
		draining: 'halfOpen',
		retired: 'pause'
	};

	function onsubmit(e: SubmitEvent) {
		e.preventDefault();
		const form = new FormData(e.currentTarget as HTMLFormElement);
		const str = (k: string) => String(form.get(k) ?? '').trim() || undefined;
		const state = str('state') as AccountState | undefined;
		const next: Filters = { q: str('q'), vendor_id: str('vendor_id'), state };
		const limit = Number(form.get('limit')) || DEFAULT_PAGE_SIZE;
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- hrefFor builds on resolve()
		goto(hrefFor(next, firstPage(limit)), { keepFocus: true, noScroll: true });
	}
</script>

<PageHeader title={section.label} summary={section.summary} />

{#if data.list === null}
	<p class="fk-note" role="note">
		Vendor accounts are internal to the platform team; tenant viewers cannot see them.
	</p>
{:else}
	<div class="fk-toolbar">
		<form class="filters" method="GET" action={base} {onsubmit} aria-label="Filter accounts">
			<div class="fk-field grow">
				<label for="a-q">Search</label>
				<input
					id="a-q"
					name="q"
					type="search"
					value={filters.q ?? ''}
					maxlength={Q_MAX}
					placeholder="account id"
				/>
			</div>
			<div class="fk-field">
				<label for="a-vendor">Vendor</label>
				<input
					id="a-vendor"
					name="vendor_id"
					value={filters.vendor_id ?? ''}
					maxlength="64"
					autocomplete="off"
					placeholder="clevertap"
				/>
			</div>
			<div class="fk-field">
				<label for="a-state">State</label>
				<select id="a-state" name="state" value={filters.state ?? ''}>
					<option value="">Any</option>
					{#each STATES as s (s)}<option value={s}>{stateLabels[s]}</option>{/each}
				</select>
			</div>
			<div class="fk-field">
				<label for="a-limit">Per page</label>
				<select id="a-limit" name="limit" value={String(paging.limit)}>
					{#each PAGE_SIZES as n (n)}<option value={String(n)}>{n}</option>{/each}
				</select>
			</div>
			<button type="submit" class="btn btn-primary">Apply</button>
			{#if filtered}
				<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- hrefFor builds on resolve() -->
				<a class="clear" href={hrefFor({})}>Clear all</a>
			{/if}
		</form>
		<RoleGate access={data.access} op="createAccount">
			<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- built on resolve() -->
			<a class="btn btn-primary" href={newHref}>Create account</a>
		</RoleGate>
	</div>

	<ListView
		result={data.list}
		noun="accounts"
		caption="Accounts"
		emptyTitle={filtered ? 'No accounts match these filters' : 'No accounts yet'}
		{filtered}
		clearHref={hrefFor({})}
		columns={[
			{ label: 'Account' },
			{ label: 'Vendor' },
			{ label: 'Region' },
			{ label: 'State' },
			{ label: 'Credential' },
			{ label: 'Credential expires' },
			{ label: 'Integrations using', numeric: true },
			{ label: 'Updated' }
		]}
		onretry={invalidateAll}
	>
		{#snippet row(a)}
			<th scope="row"
				><a href={accountHref(a.account_id)}><code class="id">{a.account_id}</code></a></th
			>
			<td>
				<a href={resolve('/(app)/vendors/[vendor_id]', { vendor_id: a.vendor_id })}
					><code>{a.vendor_id}</code></a
				>
			</td>
			<td>{a.region}</td>
			<td><StatusBadge tone={tone[a.state]} icon={icon[a.state]} label={stateLabels[a.state]} /></td
			>
			<td>
				{a.active_cred_version == null
					? 'None'
					: `v${a.active_cred_version}`}{#if a.next_cred_version != null}
					<span class="next"><Icon name="alert" size={14} />v{a.next_cred_version} waiting</span>
				{/if}
			</td>
			<td>{a.credential_expires_at ? formatUtc(a.credential_expires_at) : 'Never'}</td>
			<td class="numeric">{formatNumber(a.integrations_using)}</td>
			<td>{formatUtc(a.updated_at)}</td>
		{/snippet}
		{#snippet empty()}
			{#if filtered}
				<p>Nothing matches these filters.</p>
			{:else}
				<p>Create an account for each vendor workspace the Hub calls with its own credentials.</p>
			{/if}
		{/snippet}
		{#snippet footer(page)}
			<Pager
				{paging}
				nextCursor={page.next_cursor}
				count={page.items.length}
				noun="accounts"
				href={(p) => hrefFor(filters, p)}
			/>
		{/snippet}
	</ListView>
{/if}

<style>
	.filters {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: var(--space-3);
		flex: 1;
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
	}
	.grow {
		flex: 1;
		min-width: 12rem;
		max-width: 24rem;
	}
	.clear {
		align-self: center;
		font-size: var(--text-sm);
	}
	.next {
		display: inline-flex;
		align-items: center;
		gap: 0.2rem;
		margin-left: var(--space-2);
		color: var(--status-warn);
		font-size: var(--text-xs);
		font-weight: 700;
	}
</style>

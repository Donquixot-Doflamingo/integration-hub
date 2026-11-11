<script lang="ts">
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { TenantChange, TenantSummary, TenantSyncResult } from '$lib/api/types';
	import ChangeNotice from '$lib/components/ChangeNotice.svelte';
	import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Pager from '$lib/components/Pager.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { sectionFor } from '$lib/nav';
	import { formatNumber, formatUtc } from '$lib/utils/format';
	import {
		hasIsfcTenants,
		isfcState,
		lastSyncedText,
		latestSync,
		syncSummary
	} from '$lib/utils/isfc';
	import { DEFAULT_PAGE_SIZE, PAGE_SIZES, type Paging } from '$lib/utils/paging';
	import type { PageData } from './$types';
	import type { Unavailable } from './+page';
	import { Q_MAX, searchFor } from './filters';
	import TenantFields from './TenantFields.svelte';
	import { canSend, EMPTY_VALUES, type FieldValues } from './tenant-form';

	let { data }: { data: PageData } = $props();
	const section = sectionFor('/tenants')!;

	const q = $derived(data.state.q);
	const paging = $derived(data.state.paging);
	const base = resolve('/tenants');
	const firstPage = (limit: number): Paging => ({ cursor: undefined, prev: [], limit });
	const hrefFor = (query: string | undefined, p: Paging = firstPage(paging.limit)) =>
		base + searchFor(query, p);
	const tenantHref = (id: string) => resolve('/(app)/tenants/[tenant_id]', { tenant_id: id });

	// Tenant viewers: own tenants that getTenant did not return (listed under the table).
	let missing = $state.raw<Unavailable[]>([]);
	$effect(() => {
		let live = true;
		missing = [];
		data.unavailable.then((m) => {
			if (live) missing = m;
		});
		return () => {
			live = false;
		};
	});

	// The rows on screen, to know whether any tenant comes from ISFC (then "Sync now" is offered).
	let rows = $state.raw<TenantSummary[]>([]);
	$effect(() => {
		let live = true;
		rows = [];
		data.list.then((r) => {
			if (live) rows = r.ok ? r.data.items : [];
		});
		return () => {
			live = false;
		};
	});
	const fromIsfc = $derived(data.isfcMode || hasIsfcTenants(rows));
	const synced = $derived(latestSync(rows));

	let syncOpen = $state(false);
	function onSynced(d: ActionSuccessData) {
		const s = syncSummary(d.result as TenantSyncResult);
		notice = { message: s.message, tone: s.tone, changeId: d.changeId, requestId: d.requestId };
	}

	let createOpen = $state(false);
	let createValues = $state<FieldValues>({ ...EMPTY_VALUES });
	// A fresh form each time the dialog opens.
	$effect.pre(() => {
		if (createOpen) createValues = { ...EMPTY_VALUES };
	});
	let notice = $state.raw<{
		message: string;
		changeId: string | null;
		requestId: string | null;
		link?: { href: string; label: string };
		tone?: 'ok' | 'warn';
	} | null>(null);

	function onCreated(d: ActionSuccessData) {
		const t = (d.result as TenantChange).tenant;
		notice = {
			message: `Created tenant ${t.tenant_id} (${t.display_name}). Next: create its API keys and callback endpoints.`,
			changeId: d.changeId,
			requestId: d.requestId,
			link: { href: tenantHref(t.tenant_id), label: `Open ${t.tenant_id}` }
		};
	}

	function onsubmit(e: SubmitEvent) {
		e.preventDefault();
		const form = new FormData(e.currentTarget as HTMLFormElement);
		const next = String(form.get('q') ?? '').trim() || undefined;
		const limit = Number(form.get('limit')) || DEFAULT_PAGE_SIZE;
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- hrefFor builds on resolve()
		goto(hrefFor(next, firstPage(limit)), { keepFocus: true, noScroll: true });
	}
</script>

<PageHeader title={section.label} summary={section.summary} />

<div class="toolbar">
	<form class="filters" method="GET" action={base} {onsubmit} aria-label="Search tenants">
		<div class="field grow">
			<label for="tn-q">Search</label>
			<input
				id="tn-q"
				name="q"
				type="search"
				value={q ?? ''}
				maxlength={Q_MAX}
				placeholder="tenant id or name"
			/>
		</div>
		{#if !data.scoped}
			<div class="field">
				<label for="tn-limit">Per page</label>
				<select id="tn-limit" name="limit" value={String(paging.limit)}>
					{#each PAGE_SIZES as n (n)}<option value={String(n)}>{n}</option>{/each}
				</select>
			</div>
		{/if}
		<button type="submit" class="apply">Search</button>
		{#if q}
			<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- hrefFor builds on resolve() -->
			<a class="clear" href={hrefFor(undefined)}>Clear search</a>
		{/if}
	</form>
	<div class="buttons">
		{#if fromIsfc}
			<RoleGate access={data.access} op="syncTenants">
				<button type="button" class="secondary" onclick={() => (syncOpen = true)}>
					<Icon name="retry" size={16} />Sync now
				</button>
			</RoleGate>
		{/if}
		<!-- ISFC owns tenant creation: a tenant is linked to the Integration Hub client in ISFC. -->
		{#if !data.isfcMode}
			<RoleGate access={data.access} op="createTenant">
				<button type="button" class="primary" onclick={() => (createOpen = true)}>
					Create tenant
				</button>
			</RoleGate>
		{/if}
	</div>
</div>

{#if fromIsfc}
	<p class="note" data-testid="isfc-note">
		Tenants come from ISFC: their ISFC fields are read-only copies{#if data.isfcAdminUrl}
			(change them <!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- external ISFC URL -->
			<a href={data.isfcAdminUrl} rel="noreferrer">in ISFC</a>){:else}
			(change them in ISFC){/if}.
		{#if synced}Last synced {lastSyncedText(synced)}.{/if}
	</p>
{/if}

{#if data.scoped}
	<p class="note" role="note">You see the tenants your role is limited to.</p>
{/if}

{#if notice}
	<ChangeNotice {...notice} ondismiss={() => (notice = null)} />
{/if}

<ListView
	result={data.list}
	noun="tenants"
	caption="Tenants"
	emptyTitle={q ? 'No tenants match this search' : 'No tenants yet'}
	filtered={!!q}
	clearHref={hrefFor(undefined)}
	columns={[
		{ label: 'Tenant' },
		{ label: 'Name' },
		{ label: 'Status' },
		{ label: 'ISFC' },
		{ label: 'Integrations owned', numeric: true },
		{ label: 'Active API keys', numeric: true },
		{ label: 'Updated' }
	]}
	onretry={invalidateAll}
>
	{#snippet row(t)}
		<th scope="row"><a href={tenantHref(t.tenant_id)}><code class="id">{t.tenant_id}</code></a></th>
		<td>{t.display_name}</td>
		<td>
			{#if t.disabled}
				<StatusBadge tone="neutral" icon="pause" label="Disabled" />
			{:else}
				<StatusBadge tone="ok" icon="closed" label="Active" />
			{/if}
			{#if t.setup === 'pending'}
				<StatusBadge tone="warn" icon="alert" label="Setup pending" />
			{/if}
		</td>
		<td>
			{#if t.isfc}
				{@const st = isfcState(t.isfc)}
				<code class="id">{t.isfc.key}</code>
				<StatusBadge tone={st.tone} icon={st.icon} label={st.label} />
				<span class="meta">Synced {formatUtc(t.isfc.synced_at)}</span>
			{:else}
				<span class="meta">Hub only</span>
			{/if}
		</td>
		<td class="numeric">{formatNumber(t.integrations_owned)}</td>
		<td class="numeric">{formatNumber(t.active_api_keys)}</td>
		<td>{formatUtc(t.updated_at)}</td>
	{/snippet}
	{#snippet empty()}
		{#if q}
			<p>Nothing matches "{q}" in a tenant id or name.</p>
		{:else}
			<p>Create a tenant for each calling team before issuing API keys.</p>
		{/if}
	{/snippet}
	{#snippet footer(page)}
		{#if data.scoped}
			<p class="meta" role="status">
				{page.items.length}
				{page.items.length === 1 ? 'tenant' : 'tenants'} shown.
			</p>
		{:else}
			<Pager
				{paging}
				nextCursor={page.next_cursor}
				count={page.items.length}
				noun="tenants"
				href={(p) => hrefFor(q, p)}
			/>
		{/if}
	{/snippet}
</ListView>

{#if missing.length}
	<div class="missing" role="note">
		<p><Icon name="alert" size={16} />Some of your tenants could not be loaded:</p>
		<ul>
			{#each missing as m (m.tenantId)}
				<li>
					<code class="id">{m.tenantId}</code>: <code>{m.error.code}</code>
					{m.error.message}{#if m.error.requestId}
						(request <code class="id">{m.error.requestId}</code>){/if}
				</li>
			{/each}
		</ul>
	</div>
{/if}

<ConfirmDialog
	bind:open={syncOpen}
	title="Sync tenants from ISFC"
	verb="Sync now"
	busyVerb="Syncing..."
	action="?/syncAll"
	tone="primary"
	subject="the tenant list"
	conflictIsStale={false}
	onsuccess={onSynced}
>
	<p>
		Pulls the current state of every tenant linked to the Integration Hub from ISFC: new tenants are
		created with setup pending, changed names and statuses are copied, and tenants ISFC reports
		inactive or unlinked are disabled (never deleted). If ISFC cannot be reached, the last good
		state is kept and nothing is disabled.
	</p>
</ConfirmDialog>

<ConfirmDialog
	bind:open={createOpen}
	title="Create a tenant"
	verb="Create tenant"
	conflictIsStale={false}
	busyVerb="Creating..."
	action="?/create"
	tone="primary"
	subject="the tenant list"
	ready={canSend(createValues, EMPTY_VALUES, 'create')}
	onsuccess={onCreated}
>
	<p>
		A tenant is a calling team or service. Creating one does not issue API keys or callback
		endpoints: add those on the tenant's page.
	</p>
	{#snippet fields(errors)}
		<TenantFields mode="create" {errors} bind:values={createValues} />
	{/snippet}
</ConfirmDialog>

<style>
	.toolbar {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		justify-content: space-between;
		gap: var(--space-3);
		margin-bottom: var(--space-3);
		max-width: 72rem;
	}
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
	.field {
		display: grid;
		gap: 0.2rem;
	}
	.grow {
		flex: 1;
		min-width: 12rem;
		max-width: 28rem;
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
	.buttons {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}
	.secondary {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		height: 2.125rem;
		padding: 0 1rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		color: var(--ink);
		font-weight: 700;
		cursor: pointer;
	}
	.apply,
	.primary {
		height: 2rem;
		padding: 0 1rem;
		border: 1px solid var(--signal);
		border-radius: var(--radius-sm);
		background: var(--signal);
		color: var(--surface);
		font-weight: 700;
		cursor: pointer;
	}
	.primary {
		height: 2.125rem;
	}
	.clear {
		align-self: center;
		font-weight: 600;
		font-size: var(--text-sm);
	}
	.note {
		margin: 0 0 var(--space-3);
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.meta {
		margin: var(--space-2) 0 0;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.missing {
		margin-top: var(--space-3);
		padding: var(--space-3);
		border-left: 4px solid var(--status-warn);
		background: var(--status-warn-wash);
		font-size: var(--text-sm);
		max-width: 72rem;
	}
	.missing p {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		margin: 0 0 var(--space-1);
		font-weight: 700;
	}
	.missing ul {
		margin: 0;
		padding-left: var(--space-5);
	}
</style>

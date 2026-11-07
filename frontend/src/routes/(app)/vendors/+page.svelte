<script lang="ts">
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Pager from '$lib/components/Pager.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import { sectionFor } from '$lib/nav';
	import { formatNumber, formatUtc } from '$lib/utils/format';
	import { DEFAULT_PAGE_SIZE, PAGE_SIZES, type Paging } from '$lib/utils/paging';
	import type { PageData } from './$types';
	import { Q_MAX, searchFor } from './filters';
	import { authSchemeLabels } from './vendor-form';

	/** Vendors (HUB-290): search, cursor paging, and "Create vendor" for admins. */
	let { data }: { data: PageData } = $props();
	const section = sectionFor('/vendors')!;

	const q = $derived(data.state.q);
	const paging = $derived(data.state.paging);
	const base = resolve('/vendors');
	const firstPage = (limit: number): Paging => ({ cursor: undefined, prev: [], limit });
	const hrefFor = (query: string | undefined, p: Paging = firstPage(paging.limit)) =>
		base + searchFor(query, p);
	const vendorHref = (id: string) => resolve('/(app)/vendors/[vendor_id]', { vendor_id: id });

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

{#if data.list === null}
	<p class="fk-note" role="note">
		Vendors and their accounts are internal to the platform team; tenant viewers cannot see them.
	</p>
{:else}
	<div class="fk-toolbar">
		<form class="filters" method="GET" action={base} {onsubmit} aria-label="Search vendors">
			<div class="fk-field grow">
				<label for="v-q">Search</label>
				<input
					id="v-q"
					name="q"
					type="search"
					value={q ?? ''}
					maxlength={Q_MAX}
					placeholder="vendor id or name"
				/>
			</div>
			<div class="fk-field">
				<label for="v-limit">Per page</label>
				<select id="v-limit" name="limit" value={String(paging.limit)}>
					{#each PAGE_SIZES as n (n)}<option value={String(n)}>{n}</option>{/each}
				</select>
			</div>
			<button type="submit" class="btn btn-primary">Search</button>
			{#if q}
				<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- hrefFor builds on resolve() -->
				<a class="clear" href={hrefFor(undefined)}>Clear search</a>
			{/if}
		</form>
		<RoleGate access={data.access} op="createVendor">
			<a class="btn btn-primary" href={resolve('/(app)/vendors/new')}>Create vendor</a>
		</RoleGate>
	</div>

	<ListView
		result={data.list}
		noun="vendors"
		caption="Vendors"
		emptyTitle={q ? 'No vendors match this search' : 'No vendors yet'}
		filtered={!!q}
		clearHref={hrefFor(undefined)}
		columns={[
			{ label: 'Vendor' },
			{ label: 'Name' },
			{ label: 'Auth scheme' },
			{ label: 'Regions' },
			{ label: 'Endpoints', numeric: true },
			{ label: 'Accounts', numeric: true },
			{ label: 'Revision', numeric: true },
			{ label: 'Updated' }
		]}
		onretry={invalidateAll}
	>
		{#snippet row(v)}
			<th scope="row"
				><a href={vendorHref(v.vendor_id)}><code class="id">{v.vendor_id}</code></a></th
			>
			<td>{v.display_name}</td>
			<td>{authSchemeLabels[v.auth_scheme] ?? v.auth_scheme}</td>
			<td>{v.regions.join(', ')}</td>
			<td class="numeric">{formatNumber(v.endpoint_count)}</td>
			<td class="numeric">
				<a href="{resolve('/accounts')}?vendor_id={encodeURIComponent(v.vendor_id)}"
					>{formatNumber(v.account_count)}</a
				>
			</td>
			<td class="numeric">{formatNumber(v.revision)}</td>
			<td>{formatUtc(v.updated_at)}</td>
		{/snippet}
		{#snippet empty()}
			{#if q}
				<p>Nothing matches "{q}" in a vendor id or name.</p>
			{:else}
				<p>Add a vendor before creating its accounts and transformers.</p>
			{/if}
		{/snippet}
		{#snippet footer(page)}
			<Pager
				{paging}
				nextCursor={page.next_cursor}
				count={page.items.length}
				noun="vendors"
				href={(p) => hrefFor(q, p)}
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
		max-width: 28rem;
	}
	.clear {
		align-self: center;
		font-size: var(--text-sm);
	}
</style>

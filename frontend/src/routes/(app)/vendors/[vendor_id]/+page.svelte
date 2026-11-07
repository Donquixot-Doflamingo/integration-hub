<script lang="ts">
	import { onMount } from 'svelte';
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { VendorEndpoint, VendorEndpointChange } from '$lib/api/types';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import ChangeNotice from '$lib/components/ChangeNotice.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import { takeFlash, type Flash } from '$lib/stores/flash.svelte';
	import { formatDurationMs, formatNumber, formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';
	import EndpointDialog from '../EndpointDialog.svelte';
	import { latestByName, sortEndpoints } from '../endpoint-form';
	import { authSchemeLabels, breakerText, webhookSchemeLabels } from '../vendor-form';

	/**
	 * Vendor (HUB-290): its definition (regions, auth scheme, defaults, webhook verification) and
	 * every endpoint version. Admins edit the definition (PUT with If-Match on its own page) and
	 * add endpoints or endpoint versions (dialog; versions are immutable).
	 */
	let { data }: { data: PageData } = $props();
	const id = $derived(data.vendorId);

	let notice = $state.raw<Flash | null>(null);
	onMount(() => {
		notice = takeFlash();
	});

	let endpointOpen = $state(false);
	let endpointFrom = $state.raw<VendorEndpoint | null>(null);
	function openEndpoint(from: VendorEndpoint | null) {
		endpointFrom = from;
		endpointOpen = true;
	}
	function onEndpointCreated(d: ActionSuccessData) {
		const e = (d.result as VendorEndpointChange).endpoint;
		notice = {
			message: `Created endpoint ${e.endpoint_ref}: ${e.method} ${e.path}. Point a new transformer version at ${id}.${e.endpoint_ref} to use it.`,
			changeId: d.changeId,
			requestId: d.requestId,
			link: {
				href: resolve('/(app)/vendors/[vendor_id]/endpoints/[endpoint_ref]', {
					vendor_id: id,
					endpoint_ref: e.endpoint_ref
				}),
				label: `Open ${e.endpoint_ref}`
			}
		};
	}
	const endpointHref = (ref: string) =>
		resolve('/(app)/vendors/[vendor_id]/endpoints/[endpoint_ref]', {
			vendor_id: id,
			endpoint_ref: ref
		});
</script>

<PageHeader title={id} summary="Vendor definition and endpoint versions." />

{#if notice}
	<ChangeNotice {...notice} ondismiss={() => (notice = null)} />
{/if}

{#if data.vendor === null}
	<p class="fk-note" role="note">
		Vendors are internal to the platform team; tenant viewers cannot see them.
	</p>
{:else}
	<AsyncView result={data.vendor} noun="vendor {id}" onretry={invalidateAll}>
		{#snippet children(v)}
			{@const latest = latestByName(v.endpoints)}
			<section class="head" aria-label="Summary">
				<dl class="facts">
					<div>
						<dt>Display name</dt>
						<dd>{v.display_name}</dd>
					</div>
					<div>
						<dt>Auth scheme</dt>
						<dd>{v.auth_scheme ? (authSchemeLabels[v.auth_scheme] ?? v.auth_scheme) : '-'}</dd>
					</div>
					<div>
						<dt>Revision</dt>
						<dd>{formatNumber(v.revision)}</dd>
					</div>
					<div>
						<dt>API documentation</dt>
						<dd>
							{#if v.docs_url}
								<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- external vendor docs -->
								<a href={v.docs_url} rel="noopener noreferrer external" target="_blank"
									>{v.docs_url}</a
								>
							{:else}-{/if}
						</dd>
					</div>
					<div>
						<dt>Created</dt>
						<dd>{formatUtc(v.created_at)}</dd>
					</div>
					<div>
						<dt>Updated</dt>
						<dd>{formatUtc(v.updated_at)}</dd>
					</div>
				</dl>
				<p class="fk-note">
					<a href="{resolve('/accounts')}?vendor_id={encodeURIComponent(id)}">Accounts of {id}</a>
				</p>
				<RoleGate access={data.access} op="updateVendor">
					<div class="fk-actions">
						<a
							class="btn btn-primary"
							href={resolve('/(app)/vendors/[vendor_id]/edit', { vendor_id: id })}>Edit vendor</a
						>
					</div>
					{#snippet denied(role)}
						<p class="fk-note" role="note">
							Read only. Changing vendors and their endpoints needs the {role} role.
						</p>
					{/snippet}
				</RoleGate>
			</section>

			<section class="block" aria-labelledby="regions-h">
				<h2 id="regions-h">Regions</h2>
				<p class="fk-note">The only hosts the Hub may call for this vendor.</p>
				<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
				<div class="table-scroll" role="region" aria-label="Region base URLs" tabindex="0">
					<table>
						<thead><tr><th scope="col">Region</th><th scope="col">Base URL</th></tr></thead>
						<tbody>
							{#each Object.entries(v.regions ?? {}) as [code, url] (code)}
								<tr><th scope="row"><code>{code}</code></th><td><code>{url}</code></td></tr>
							{/each}
						</tbody>
					</table>
				</div>
			</section>

			<section class="block" aria-labelledby="defaults-h">
				<h2 id="defaults-h">Defaults</h2>
				<dl class="facts">
					<div>
						<dt>Rate limit per account</dt>
						<dd>
							{v.defaults?.rate_limit?.per_account_rps != null
								? `${formatNumber(v.defaults.rate_limit.per_account_rps)} requests/s`
								: 'Hub default'}
						</dd>
					</div>
					<div>
						<dt>Circuit breaker</dt>
						<dd>{breakerText(v.defaults?.breaker)}</dd>
					</div>
					<div>
						<dt>Health probe</dt>
						<dd>
							{#if v.defaults?.health_probe?.endpoint}
								<code>{v.defaults.health_probe.endpoint}</code>{v.defaults.health_probe.dry
									? ', dry'
									: ''}{v.defaults.health_probe.every_ms
									? `, every ${formatDurationMs(v.defaults.health_probe.every_ms)}`
									: ''}
							{:else}Not set{/if}
						</dd>
					</div>
					<div>
						<dt>Inbound webhook verification</dt>
						<dd>
							{#if v.webhook_verification}
								{webhookSchemeLabels[v.webhook_verification.scheme]}{v.webhook_verification.header
									? ` (${v.webhook_verification.header})`
									: ''}
							{:else}Not set{/if}
						</dd>
					</div>
				</dl>
			</section>

			<section class="block" aria-labelledby="endpoints-h">
				<div class="block-head">
					<h2 id="endpoints-h">Endpoints</h2>
					<RoleGate access={data.access} op="createVendorEndpoint">
						<button type="button" class="btn" onclick={() => openEndpoint(null)}
							>Add endpoint</button
						>
					</RoleGate>
				</div>
				<p class="fk-note">
					Every version is kept and never changes. Transformers target one version (<code
						>{id}.upload@1</code
					>).
				</p>
				{#if v.endpoints.length === 0}
					<p class="empty" role="status">
						No endpoints yet. Add the vendor API calls your transformers will make.
					</p>
				{:else}
					<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
					<div class="table-scroll" role="region" aria-label="Endpoint versions" tabindex="0">
						<table>
							<caption class="visually-hidden">Endpoint versions of {id}</caption>
							<thead>
								<tr>
									<th scope="col">Endpoint</th>
									<th scope="col">Call</th>
									<th scope="col" class="num">Timeout</th>
									<th scope="col">Idempotent</th>
									<th scope="col" class="num">Batch limit</th>
									<th scope="col">Created</th>
									<th scope="col"><span class="visually-hidden">Actions</span></th>
								</tr>
							</thead>
							<tbody>
								{#each sortEndpoints(v.endpoints) as e (e.endpoint_ref)}
									{@const isLatest = latest.get(e.name)?.version === e.version}
									<tr>
										<th scope="row">
											<a href={endpointHref(e.endpoint_ref)}><code>{e.endpoint_ref}</code></a>
											{#if isLatest}<span class="tag">Latest</span>{/if}
										</th>
										<td><code>{e.method} {e.path}</code></td>
										<td class="num">{formatDurationMs(e.timeout_ms)}</td>
										<td>{e.idempotent ? 'Yes' : 'No'}</td>
										<td class="num">{e.batch_max == null ? '-' : formatNumber(e.batch_max)}</td>
										<td>{formatUtc(e.created_at)}</td>
										<td>
											{#if isLatest}
												<RoleGate access={data.access} op="createVendorEndpoint">
													<button
														type="button"
														class="btn btn-quiet"
														onclick={() => openEndpoint(e)}
														aria-label="New version of {e.name} (creates {e.name}@{e.version + 1})"
														>New version</button
													>
												</RoleGate>
											{/if}
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{/if}
			</section>
		{/snippet}
	</AsyncView>

	<EndpointDialog
		bind:open={endpointOpen}
		vendorId={id}
		from={endpointFrom}
		onsuccess={onEndpointCreated}
	/>
{/if}

<style>
	.head {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: var(--space-3);
		max-width: 72rem;
		padding-bottom: var(--space-4);
		border-bottom: 1px solid var(--rule);
	}
	.block {
		display: grid;
		/* minmax(0, ...): a wide table scrolls inside its region instead of widening the page. */
		grid-template-columns: minmax(0, 1fr);
		gap: var(--space-2);
		margin-top: var(--space-5);
		max-width: 72rem;
	}
	.block h2 {
		font-size: var(--text-lg);
	}
	.block-head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
	}
	.empty {
		margin: 0;
		padding: var(--space-3);
		border: 1px dashed var(--rule-strong);
		border-radius: var(--radius-md);
		font-size: var(--text-sm);
	}
	.table-scroll {
		/* Contains the visually hidden header text, which is absolutely positioned. */
		position: relative;
		overflow-x: auto;
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: var(--text-sm);
	}
	th,
	td {
		height: var(--row-height);
		padding: 0 var(--space-3);
		text-align: left;
		white-space: nowrap;
		border-bottom: 1px solid var(--rule);
	}
	thead th {
		background: var(--surface-sunken);
		color: var(--ink-muted);
		font-size: var(--text-xs);
		font-weight: 600;
	}
	.num {
		text-align: right;
		font-variant-numeric: tabular-nums;
	}
	.tag {
		margin-left: var(--space-2);
		padding: 0 0.4rem;
		border: 1px solid var(--signal);
		border-radius: var(--radius-sm);
		color: var(--signal);
		font-size: var(--text-xs);
		font-weight: 700;
	}
</style>

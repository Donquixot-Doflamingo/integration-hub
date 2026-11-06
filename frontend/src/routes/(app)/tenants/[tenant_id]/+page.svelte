<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { Tenant, TenantChange } from '$lib/api/types';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import ChangeNotice from '$lib/components/ChangeNotice.svelte';
	import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { formatNumber, formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';
	import TenantFields from '../TenantFields.svelte';
	import ApiKeys from './ApiKeys.svelte';
	import CallbackEndpoints from './CallbackEndpoints.svelte';
	import {
		canSend,
		EMPTY_VALUES,
		rateLimitText,
		valuesFromTenant,
		type FieldValues
	} from '../tenant-form';

	let { data }: { data: PageData } = $props();

	const id = $derived(data.tenantId);
	const integrationsHref = $derived(
		`${resolve('/integrations')}?tenant_id=${encodeURIComponent(id)}`
	);

	// The tenant as last loaded, with its ETag, for the dialogs (If-Match, the values they open with).
	let current = $state.raw<{ tenant: Tenant; etag: string | null } | null>(null);
	$effect(() => {
		let live = true;
		data.tenant.then((r) => {
			if (live) current = r.ok ? { tenant: r.data, etag: r.etag } : null;
		});
		return () => {
			live = false;
		};
	});

	let notice = $state.raw<{
		message: string;
		changeId: string | null;
		requestId: string | null;
	} | null>(null);

	let editOpen = $state(false);
	let editBefore = $state.raw<FieldValues>(EMPTY_VALUES);
	let editValues = $state<FieldValues>({ ...EMPTY_VALUES });
	// Opens with the tenant as last loaded.
	$effect.pre(() => {
		if (editOpen && current) {
			const before = valuesFromTenant(current.tenant);
			editBefore = before;
			editValues = { ...before };
		}
	});
	let disableOpen = $state(false);

	function onUpdated(d: ActionSuccessData) {
		const t = (d.result as TenantChange).tenant;
		notice = {
			message: `Saved ${t.tenant_id}: ${t.display_name}, rate limit ${rateLimitText(t.rate_limit)}.`,
			changeId: d.changeId,
			requestId: d.requestId
		};
	}
	function onDisabledSwitched(d: ActionSuccessData) {
		const t = (d.result as TenantChange).tenant;
		notice = {
			message: t.disabled
				? `Disabled ${t.tenant_id}. Within about 10 s, every call with its API keys gets 401.`
				: `Enabled ${t.tenant_id}. Its active API keys work again within about 10 s.`,
			changeId: d.changeId,
			requestId: d.requestId
		};
	}
</script>

<PageHeader title={id} />

{#if notice}
	<ChangeNotice {...notice} ondismiss={() => (notice = null)} />
{/if}

<AsyncView result={data.tenant} noun="tenant {id}" onretry={invalidateAll}>
	{#snippet children(t)}
		<section class="head" aria-label="Summary">
			<div class="status-line">
				{#if t.disabled}
					<StatusBadge tone="neutral" icon="pause" label="Disabled" />
				{:else}
					<StatusBadge tone="ok" icon="closed" label="Active" />
				{/if}
				<p class="name">{t.display_name}</p>
			</div>
			{#if t.disabled}
				<p class="disabled" role="note">
					Disabled: calls with this tenant's API keys answer 401 until it is enabled again.
				</p>
			{/if}
			<dl class="facts">
				<div>
					<dt>Description</dt>
					<dd>{t.description || '-'}</dd>
				</div>
				<div>
					<dt>Contact</dt>
					<dd>{t.contact || '-'}</dd>
				</div>
				<div>
					<dt>Rate limit</dt>
					<dd>{rateLimitText(t.rate_limit)}</dd>
				</div>
				<div>
					<dt>Integrations owned</dt>
					<dd>
						<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- built on resolve() -->
						<a href={integrationsHref}>{formatNumber(t.integrations_owned)}</a>
					</dd>
				</div>
				<div>
					<dt>Active API keys</dt>
					<dd>{formatNumber(t.active_api_keys)}</dd>
				</div>
				<div>
					<dt>Created</dt>
					<dd>{formatUtc(t.created_at)}</dd>
				</div>
				<div>
					<dt>Updated</dt>
					<dd>{formatUtc(t.updated_at)}</dd>
				</div>
			</dl>
			<p class="meta">
				Which other tenants may call an integration (its grants) is set on each integration, not
				here.
				<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- built on resolve() -->
				<a href={integrationsHref}>Integrations owned by {t.tenant_id}</a>
			</p>
			<RoleGate access={data.access} op="updateTenant">
				<div class="actions">
					<div class="group" role="group" aria-label="Change this tenant">
						<button type="button" class="primary" onclick={() => (editOpen = true)}>
							Edit tenant
						</button>
					</div>
					<div class="group danger" role="group" aria-label="Disable this tenant">
						{#if t.disabled}
							<button type="button" onclick={() => (disableOpen = true)}>
								<Icon name="closed" size={16} />Enable tenant
							</button>
						{:else}
							<button type="button" class="stop" onclick={() => (disableOpen = true)}>
								<Icon name="pause" size={16} />Disable tenant
							</button>
						{/if}
					</div>
				</div>
				{#snippet denied(role)}
					<p class="readonly" role="note">
						Read only. Changing tenants, their API keys and callback endpoints needs the {role} role.
					</p>
				{/snippet}
			</RoleGate>
		</section>
	{/snippet}
</AsyncView>

<CallbackEndpoints
	tenantId={id}
	access={data.access}
	endpoints={data.endpoints}
	onnotice={(n) => (notice = n)}
/>

<ApiKeys
	tenantId={id}
	access={data.access}
	keys={data.keys}
	keyState={data.keyState}
	onnotice={(n) => (notice = n)}
/>

{#if current}
	{@const t = current.tenant}
	<ConfirmDialog
		bind:open={editOpen}
		title="Edit tenant {t.tenant_id}"
		verb="Save changes"
		busyVerb="Saving..."
		action="?/update"
		tone="primary"
		subject={t.tenant_id}
		ready={canSend(editValues, editBefore, 'edit')}
		onsuccess={onUpdated}
	>
		<p>
			Only the fields you change are sent. A new rate limit applies on every gateway node within
			about 10 s.
		</p>
		{#snippet fields(errors)}
			<input type="hidden" name="if_match" value={current?.etag ?? ''} />
			<TenantFields mode="edit" before={editBefore} {errors} bind:values={editValues} />
		{/snippet}
	</ConfirmDialog>

	{#if t.disabled}
		<ConfirmDialog
			bind:open={disableOpen}
			title="Enable tenant {t.tenant_id}"
			verb="Enable"
			busyVerb="Enabling..."
			action="?/setDisabled"
			tone="primary"
			subject={t.tenant_id}
			onsuccess={onDisabledSwitched}
		>
			<p>
				Calls with the tenant's active API keys are accepted again within about 10 s. Revoked or
				expired keys stay revoked.
			</p>
			{#snippet fields()}
				<input type="hidden" name="disabled" value="false" />
				<input type="hidden" name="if_match" value={current?.etag ?? ''} />
			{/snippet}
		</ConfirmDialog>
	{:else}
		<ConfirmDialog
			bind:open={disableOpen}
			title="Disable tenant {t.tenant_id}"
			verb="Disable"
			busyVerb="Disabling..."
			action="?/setDisabled"
			confirmText={t.tenant_id}
			subject={t.tenant_id}
			onsuccess={onDisabledSwitched}
		>
			<p>
				Within about 10 s, every call made with this tenant's {formatNumber(t.active_api_keys)} active
				API {t.active_api_keys === 1 ? 'key' : 'keys'} answers 401. Its integrations, keys and callback
				endpoints are kept; enabling the tenant again restores access.
			</p>
			{#snippet fields()}
				<input type="hidden" name="disabled" value="true" />
				<input type="hidden" name="if_match" value={current?.etag ?? ''} />
			{/snippet}
		</ConfirmDialog>
	{/if}
{/if}

<style>
	.head {
		display: grid;
		gap: var(--space-3);
		max-width: 72rem;
		padding-bottom: var(--space-4);
		border-bottom: 1px solid var(--rule);
	}
	.status-line {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-3);
	}
	.name {
		margin: 0;
		font-weight: 700;
	}
	.disabled {
		margin: 0;
		padding: var(--space-2) var(--space-3);
		border-left: 4px solid var(--status-neutral);
		background: var(--status-neutral-wash);
		font-size: var(--text-sm);
	}
	.meta,
	.readonly {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
	}
	.group {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}
	.group.danger {
		padding-left: var(--space-3);
		border-left: 1px solid var(--rule);
	}
	.actions button {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		min-height: 2.125rem;
		padding: 0 0.9rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		color: var(--ink);
		font-weight: 700;
		cursor: pointer;
	}
	.actions .primary {
		border-color: var(--signal);
		background: var(--signal);
		color: var(--surface);
	}
	.actions .stop {
		border-color: var(--status-danger);
		color: var(--status-danger);
	}
</style>

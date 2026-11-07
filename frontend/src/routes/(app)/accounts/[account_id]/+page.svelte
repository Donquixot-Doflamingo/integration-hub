<script lang="ts">
	import { onMount } from 'svelte';
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { Account, AccountChange, AccountState } from '$lib/api/types';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import ChangeNotice from '$lib/components/ChangeNotice.svelte';
	import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
	import FieldError from '$lib/components/FieldError.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { takeFlash, type Flash } from '$lib/stores/flash.svelte';
	import { formatNumber, formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';
	import CredentialsPanel from './CredentialsPanel.svelte';
	import {
		editProblems,
		editValuesFromAccount,
		hasEditChanges,
		type EditValues
	} from '../account-form';
	import { stateLabels } from '../filters';

	/**
	 * Account (HUB-291): lifecycle state, config, quota, credential slots and rotation, the
	 * integrations that use it. Admins edit config and quota (merge-patch with If-Match), rotate
	 * credentials and move the lifecycle (active -> draining -> retired); editors test the
	 * connection. Every control renders only for roles the contract allows.
	 */
	let { data }: { data: PageData } = $props();
	const id = $derived(data.accountId);

	let notice = $state.raw<Flash | null>(null);
	onMount(() => {
		notice = takeFlash();
	});

	let current = $state.raw<{ account: Account; etag: string | null } | null>(null);
	$effect(() => {
		let live = true;
		data.account?.then((r) => {
			if (live) current = r.ok ? r.data : null;
		});
		return () => {
			live = false;
		};
	});
	let endpoints = $state.raw<string[]>([]);
	let noCredentials = $state(false);
	$effect(() => {
		let live = true;
		data.endpoints.then((e) => {
			if (live) endpoints = e;
		});
		data.authScheme.then((s) => {
			if (live) noCredentials = s === 'none';
		});
		return () => {
			live = false;
		};
	});

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

	// Edit config and quota.
	let editOpen = $state(false);
	let before = $state.raw<EditValues>({ config: '{}', rps: '', daily: '' });
	let edit = $state<EditValues>({ config: '{}', rps: '', daily: '' });
	// Opens with the account as last loaded; later reloads do not reset what is being typed.
	function openEdit(a: Account) {
		const b = editValuesFromAccount(a);
		before = b;
		edit = { ...b };
		editOpen = true;
	}
	const editFound = $derived(editProblems(edit));

	// Lifecycle.
	let lifecycleOpen = $state(false);
	let target = $state<AccountState>('draining');
	function openLifecycle(t: AccountState) {
		target = t;
		lifecycleOpen = true;
	}
	const lifecycleText: Record<AccountState, { verb: string; busy: string }> = {
		active: { verb: 'Return to active', busy: 'Returning...' },
		draining: { verb: 'Start draining', busy: 'Draining...' },
		retired: { verb: 'Retire account', busy: 'Retiring...' }
	};

	function onAccountChange(verb: string) {
		return (d: ActionSuccessData) => {
			const a = (d.result as AccountChange).account;
			notice = {
				message: `${verb} ${a.account_id}: state ${stateLabels[a.state]}, quota ${quotaText(a)}.`,
				changeId: d.changeId,
				requestId: d.requestId
			};
		};
	}
	const quotaText = (a: Account) =>
		[
			a.quota.rps != null ? `${formatNumber(a.quota.rps)} requests/s` : null,
			a.quota.daily != null ? `${formatNumber(a.quota.daily)} requests/day` : null
		]
			.filter(Boolean)
			.join(', ') || 'no limit set';
</script>

<PageHeader title={id} summary="A credentialed vendor account." />

{#if notice}
	<ChangeNotice {...notice} ondismiss={() => (notice = null)} />
{/if}

{#if data.account === null}
	<p class="fk-note" role="note">
		Vendor accounts are internal to the platform team; tenant viewers cannot see them.
	</p>
{:else}
	<AsyncView result={data.account} noun="account {id}" onretry={invalidateAll}>
		{#snippet children({ account: a })}
			<section class="head" aria-label="Summary">
				<div class="status-line">
					<StatusBadge tone={tone[a.state]} icon={icon[a.state]} label={stateLabels[a.state]} />
					{#if a.state === 'draining'}
						<span class="fk-note"
							>No new integration versions should target it; retire it when unused.</span
						>
					{:else if a.state === 'retired'}
						<span class="fk-note">Retired: credentials deleted, it can no longer be used.</span>
					{/if}
				</div>
				<dl class="facts">
					<div>
						<dt>Vendor</dt>
						<dd>
							<a href={resolve('/(app)/vendors/[vendor_id]', { vendor_id: a.vendor_id })}
								><code>{a.vendor_id}</code></a
							>
						</dd>
					</div>
					<div>
						<dt>Region</dt>
						<dd>{a.region}</dd>
					</div>
					<div>
						<dt>Quota</dt>
						<dd>{quotaText(a)}</dd>
					</div>
					<div>
						<dt>Pinned requests</dt>
						<dd>{formatNumber(a.pinned_requests)}</dd>
					</div>
					<div>
						<dt>Open vendor sessions</dt>
						<dd>{formatNumber(a.open_sessions)}</dd>
					</div>
					<div>
						<dt>Dead letters</dt>
						<dd>
							{#if a.dead_letter_count == null}Unknown{:else if a.dead_letter_count === 0}0{:else}
								<a href="{resolve('/dlq')}?account_id={encodeURIComponent(a.account_id)}"
									>{formatNumber(a.dead_letter_count)}</a
								>{/if}
						</dd>
					</div>
					<div>
						<dt>Created</dt>
						<dd>{formatUtc(a.created_at)}</dd>
					</div>
					<div>
						<dt>Updated</dt>
						<dd>{formatUtc(a.updated_at)}</dd>
					</div>
				</dl>
				<div class="config">
					<h2 class="sub">
						Config <span class="fk-req"
							>(not secret; transformers read <code>account.config.*</code>)</span
						>
					</h2>
					<pre><code>{JSON.stringify(a.config ?? {}, null, 2)}</code></pre>
				</div>
				{#if a.state !== 'retired'}
					<RoleGate access={data.access} op="updateAccount">
						<div class="fk-actions">
							<button type="button" class="btn btn-primary" onclick={() => openEdit(a)}
								>Edit config and quota</button
							>
						</div>
						{#snippet denied(role)}
							<p class="fk-note" role="note">
								Read only. Editing accounts and rotating their credentials needs the {role} role.
							</p>
						{/snippet}
					</RoleGate>
				{/if}
			</section>

			<CredentialsPanel
				account={a}
				{endpoints}
				{noCredentials}
				access={data.access}
				onnotice={(n) => (notice = n)}
			/>

			<section class="block" aria-labelledby="using-h">
				<h2 id="using-h">Used by</h2>
				{#if a.integrations_using.length === 0}
					<p class="fk-note">No integration version targets this account.</p>
				{:else}
					<ul class="using">
						{#each a.integrations_using as u (`${u.integration_id}@${u.version}:${u.role}`)}
							<li>
								<a
									href={resolve('/(app)/integrations/[integration_id]/versions/[version]', {
										integration_id: u.integration_id,
										version: String(u.version)
									})}><code>{u.integration_id}</code> v{u.version}</a
								>
								as {u.role}
							</li>
						{/each}
					</ul>
				{/if}
			</section>

			{#if a.state !== 'retired'}
				<RoleGate access={data.access} op="transitionAccountLifecycle">
					<section class="block danger" aria-labelledby="life-h">
						<h2 id="life-h">Lifecycle</h2>
						<p class="fk-note">
							Active accounts take traffic. Draining ones keep serving what still targets them.
							Retiring deletes every credential and cannot be undone.
						</p>
						<div class="fk-actions">
							{#if a.state === 'active'}
								<button type="button" class="btn" onclick={() => openLifecycle('draining')}>
									<Icon name="halfOpen" size={16} />Start draining
								</button>
							{:else}
								<button type="button" class="btn" onclick={() => openLifecycle('active')}>
									<Icon name="closed" size={16} />Return to active
								</button>
								<button
									type="button"
									class="btn btn-danger"
									onclick={() => openLifecycle('retired')}
								>
									<Icon name="pause" size={16} />Retire account
								</button>
							{/if}
						</div>
					</section>
				</RoleGate>
			{/if}
		{/snippet}
	</AsyncView>

	{#if current}
		{@const a = current.account}
		<ConfirmDialog
			bind:open={editOpen}
			title="Edit config and quota of {a.account_id}"
			verb="Save changes"
			busyVerb="Saving..."
			action="?/update"
			tone="primary"
			subject={a.account_id}
			ready={Object.keys(editFound).length === 0 && hasEditChanges(edit, before)}
			onsuccess={onAccountChange('Saved')}
		>
			<p>
				Only what you change is sent (a merge-patch: removed config keys are deleted). Vendor,
				region and credentials do not change here.
			</p>
			{#snippet fields(errors)}
				<input type="hidden" name="if_match" value={current?.etag ?? ''} />
				<input type="hidden" name="before.config" value={before.config} />
				<input type="hidden" name="before.rps" value={before.rps} />
				<input type="hidden" name="before.daily" value={before.daily} />
				<div class="fk-field">
					<label for="{a.account_id}-config">Config (JSON object)</label>
					<textarea
						id="{a.account_id}-config"
						name="config"
						class="fk-mono"
						rows="7"
						bind:value={edit.config}
						spellcheck="false"
						aria-invalid={errors['/config'] || editFound['/config'] ? 'true' : undefined}
						aria-describedby="{a.account_id}-config-error"></textarea>
					<FieldError
						id="{a.account_id}-config-error"
						message={errors['/config'] ?? editFound['/config']}
					/>
				</div>
				<div class="fk-grid">
					<div class="fk-field">
						<label for="{a.account_id}-rps">Quota (requests/s)</label>
						<input
							id="{a.account_id}-rps"
							name="rps"
							inputmode="decimal"
							bind:value={edit.rps}
							aria-invalid={errors['/quota/rps'] || editFound['/quota/rps'] ? 'true' : undefined}
							aria-describedby="{a.account_id}-rps-error"
						/>
						<FieldError
							id="{a.account_id}-rps-error"
							message={errors['/quota/rps'] ?? editFound['/quota/rps']}
						/>
					</div>
					<div class="fk-field">
						<label for="{a.account_id}-daily">Quota (requests/day)</label>
						<input
							id="{a.account_id}-daily"
							name="daily"
							inputmode="numeric"
							bind:value={edit.daily}
							aria-invalid={errors['/quota/daily'] || editFound['/quota/daily']
								? 'true'
								: undefined}
							aria-describedby="{a.account_id}-daily-error"
						/>
						<FieldError
							id="{a.account_id}-daily-error"
							message={errors['/quota/daily'] ?? editFound['/quota/daily']}
						/>
					</div>
				</div>
			{/snippet}
		</ConfirmDialog>

		<ConfirmDialog
			bind:open={lifecycleOpen}
			title="{lifecycleText[target].verb}: {a.account_id}"
			verb={lifecycleText[target].verb}
			busyVerb={lifecycleText[target].busy}
			action="?/lifecycle"
			tone={target === 'retired' ? 'danger' : 'primary'}
			confirmText={target === 'retired' ? a.account_id : undefined}
			subject={a.account_id}
			conflictIsStale={false}
			onsuccess={onAccountChange(target === 'retired' ? 'Retired' : 'Moved')}
		>
			{#if target === 'retired'}
				<p>
					Deletes every credential of {a.account_id} and takes it out of service for good. The Hub refuses
					(RETIRE_BLOCKED) while an active integration version targets it, or while pinned requests ({formatNumber(
						a.pinned_requests
					)}) or open vendor sessions ({formatNumber(a.open_sessions)}) use it.
				</p>
			{:else if target === 'draining'}
				<p>
					Marks {a.account_id} as draining: what still targets it keeps working; plan its replacement
					and retire it when nothing uses it ({a.integrations_using.length} integration
					{a.integrations_using.length === 1 ? 'version' : 'versions'} now).
				</p>
			{:else}
				<p>{a.account_id} goes back to active and may be targeted by new versions again.</p>
			{/if}
			{#snippet fields()}
				<input type="hidden" name="target_state" value={target} />
			{/snippet}
		</ConfirmDialog>
	{/if}
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
	.status-line {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-3);
	}
	.sub {
		font-size: var(--text-sm);
		font-weight: 700;
	}
	pre {
		margin: var(--space-1) 0 0;
		padding: var(--space-2) var(--space-3);
		overflow-x: auto;
		border: 1px solid var(--rule);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-sm);
	}
	.block {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: var(--space-2);
		margin-top: var(--space-5);
		max-width: 72rem;
	}
	.block h2 {
		font-size: var(--text-lg);
	}
	.block.danger {
		padding-top: var(--space-4);
		border-top: 1px solid var(--rule);
	}
	.using {
		margin: 0;
		padding-left: var(--space-4);
		font-size: var(--text-sm);
	}
</style>

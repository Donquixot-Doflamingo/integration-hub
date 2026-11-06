<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { Integration, IntegrationChange, PublishResult } from '$lib/api/types';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import ChangeNotice from '$lib/components/ChangeNotice.svelte';
	import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
	import ListView from '$lib/components/ListView.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import SpecView from '$lib/components/integration/SpecView.svelte';
	import { can } from '$lib/auth/roles';
	import { integrationStatus } from '$lib/status';
	import { formatUtc } from '$lib/utils/format';
	import type { PageData } from './$types';
	import WeightsDialog from './WeightsDialog.svelte';

	let { data }: { data: PageData } = $props();
	// HUB-288: rendered only for roles that may call rollbackIntegration (contract x-required-role).
	const canRollback = $derived(can(data.access, 'rollbackIntegration'));

	const id = $derived(data.integrationId);
	const diffBase = $derived(
		resolve('/(app)/integrations/[integration_id]/diff', { integration_id: id })
	);
	const publishHref = $derived(
		resolve('/(app)/integrations/[integration_id]/publish', { integration_id: id })
	);
	const versionHref = (v: number) =>
		resolve('/(app)/integrations/[integration_id]/versions/[version]', {
			integration_id: id,
			version: String(v)
		});

	// The integration as last loaded, for the mutation dialogs (expected_active_version, targets).
	let current = $state.raw<Integration | null>(null);
	$effect(() => {
		let live = true;
		data.integration.then((r) => {
			if (live) current = r.ok ? r.data : null;
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

	// HUB-259: rollback = publish a copy of an older version as a new version.
	let rollbackOpen = $state(false);
	let rollbackTo = $state<number | null>(null);
	function openRollback(version: number) {
		rollbackTo = version;
		rollbackOpen = true;
	}
	function onRolledBack(d: ActionSuccessData) {
		const r = d.result as PublishResult;
		notice = {
			message: `Rolled back: v${r.active_version} is now active, a copy of v${r.copied_from_version ?? rollbackTo}. v${r.previous_active_version ?? '?'} stays in the history.`,
			changeId: d.changeId,
			requestId: d.requestId
		};
	}

	// HUB-260: weights (a new version) and the pause kill switch.
	let weightsOpen = $state(false);
	let pauseOpen = $state(false);
	function onWeights(d: ActionSuccessData) {
		const r = d.result as PublishResult;
		const created = r.created_version ?? r.active_version;
		notice = {
			message:
				r.active_version !== r.previous_active_version
					? `Weights set: v${r.active_version} is now active (copy of v${r.copied_from_version ?? r.previous_active_version} with the new weights).`
					: `Weights saved as draft v${created}. v${r.active_version} stays active until you publish it.`,
			changeId: d.changeId,
			requestId: d.requestId
		};
	}
	function onPauseSwitched(d: ActionSuccessData) {
		const i = (d.result as IntegrationChange).integration;
		notice = {
			message: i.paused
				? `Paused ${i.integration_id}. Within about 10 s, sync and wait calls get 503 INTEGRATION_PAUSED and workers hold queued requests.`
				: `Resumed ${i.integration_id}. Calls are served again and held requests resume on their normal schedule.`,
			changeId: d.changeId,
			requestId: d.requestId
		};
	}
</script>

<PageHeader title={id} />

{#if notice}
	<ChangeNotice {...notice} ondismiss={() => (notice = null)} />
{/if}

<AsyncView result={data.integration} noun="integration {id}" onretry={invalidateAll}>
	{#snippet children(i)}
		{@const s = integrationStatus(i)}
		<section class="head" aria-label="Summary">
			<div class="status-line">
				<StatusBadge tone={s.tone} icon={s.icon} label={s.label} />
				{#if i.description}<p class="desc">{i.description}</p>{/if}
			</div>
			{#if i.paused && i.paused_reason}
				<p class="paused" role="note">Paused: {i.paused_reason}</p>
			{/if}
			<dl class="facts">
				<div>
					<dt>Owner tenant</dt>
					<dd>{i.owner_tenant}</dd>
				</div>
				<div>
					<dt>Capability</dt>
					<dd><code>{i.capability}</code></dd>
				</div>
				<div>
					<dt>Active version</dt>
					<dd>{i.active_version == null ? 'None' : `v${i.active_version}`}</dd>
				</div>
				<div>
					<dt>Latest version</dt>
					<dd>
						{i.latest_version == null ? 'None' : `v${i.latest_version}`}{i.has_draft
							? ' (draft)'
							: ''}
					</dd>
				</div>
				<div>
					<dt>Other tenants allowed</dt>
					<dd>{i.grants.length ? i.grants.join(', ') : 'None'}</dd>
				</div>
				<div>
					<dt>Moves money or quota</dt>
					<dd>{i.moves_money_or_quota ? 'Yes: callers must send an Idempotency-Key' : 'No'}</dd>
				</div>
				<div>
					<dt>Created</dt>
					<dd>{formatUtc(i.created_at)}</dd>
				</div>
				<div>
					<dt>Updated</dt>
					<dd>{formatUtc(i.updated_at)}</dd>
				</div>
			</dl>
			<RoleGate
				access={data.access}
				ops={['createIntegrationDraft', 'pauseIntegration', 'resumeIntegration']}
			>
				<div class="actions">
					<div class="group" role="group" aria-label="Change this integration">
						<a class="primary" href={publishHref}>
							{i.has_draft ? `Review draft v${i.latest_version} and publish` : 'Edit a new draft'}
						</a>
						{#if i.active && i.active.spec.targets.length > 1 && can(data.access, 'updateIntegrationWeights')}
							<button type="button" onclick={() => (weightsOpen = true)}>Set weights</button>
						{/if}
					</div>
					<div class="group danger" role="group" aria-label="Kill switch">
						{#if i.paused}
							<button type="button" onclick={() => (pauseOpen = true)}>
								<Icon name="closed" size={16} />Resume integration
							</button>
						{:else}
							<button type="button" class="stop" onclick={() => (pauseOpen = true)}>
								<Icon name="pause" size={16} />Pause integration
							</button>
						{/if}
					</div>
				</div>
				{#snippet denied(role)}
					<p class="readonly" role="note">
						Read only. Publishing, rolling back, setting weights and pausing need the {role} role.
					</p>
				{/snippet}
			</RoleGate>
		</section>

		<section class="block" aria-labelledby="active-heading">
			{#if i.active}
				{@const a = i.active}
				<h2 id="active-heading">Active version v{a.version}</h2>
				<p class="meta">
					Published {formatUtc(a.published_at)} by {a.published_by ?? a.author}. Reason: {a.reason}.
					{#if a.snapshot_version != null}Snapshot {a.snapshot_version}.{/if}
				</p>
				<SpecView spec={a.spec} />
			{:else}
				<h2 id="active-heading">Active version</h2>
				<p class="meta">None. Invocations answer 503 until a first version is published.</p>
			{/if}
		</section>
	{/snippet}
</AsyncView>

<section class="block" aria-labelledby="history-heading">
	<h2 id="history-heading">Version history</h2>
	<p class="meta">
		Every version, newest first. Rollback publishes a copy, so nothing is ever removed. Pick a From
		and a To version to compare them.
	</p>
	<form method="GET" action={diffBase} aria-label="Compare versions">
		<ListView
			result={data.versions}
			noun="versions"
			caption="Versions of {id}, newest first"
			emptyTitle="No versions yet"
			columns={[
				{ label: 'Version', numeric: true },
				{ label: 'State' },
				...(canRollback ? [{ label: 'Actions' }] : []),
				{ label: 'Author' },
				{ label: 'Reason' },
				{ label: 'Created' },
				{ label: 'Published' },
				{ label: 'Based on', numeric: true },
				{ label: 'From' },
				{ label: 'To' },
				{ label: 'Changes' }
			]}
			onretry={invalidateAll}
		>
			{#snippet row(v)}
				<th scope="row" class="numeric" class:active={v.is_active}>
					<a href={versionHref(v.version)}>v{v.version}</a>
				</th>
				<td>
					{#if v.is_active}
						<StatusBadge tone="ok" icon="check" label="Active" />
					{:else if v.state === 'draft'}
						<StatusBadge tone="warn" icon="changelog" label="Draft" />
					{:else}
						<span class="muted">Published</span>
					{/if}
				</td>
				{#if canRollback}
					<td>
						{#if v.state === 'published' && !v.is_active && current?.active_version != null}
							<button type="button" class="row-action" onclick={() => openRollback(v.version)}>
								Roll back to v{v.version}
							</button>
						{:else}<span class="muted">-</span>{/if}
					</td>
				{/if}
				<td>{v.author}</td>
				<td class="reason" title={v.reason}>{v.reason}</td>
				<td>{formatUtc(v.created_at)}</td>
				<td>{v.published_at ? formatUtc(v.published_at) : '-'}</td>
				<td class="numeric">{v.based_on == null ? '-' : `v${v.based_on}`}</td>
				<td class="pick">
					<input
						type="radio"
						name="from"
						value={String(v.version)}
						checked={v.is_active}
						aria-label="Compare from v{v.version}"
					/>
				</td>
				<td class="pick">
					<input
						type="radio"
						name="to"
						value={String(v.version)}
						aria-label="Compare to v{v.version}"
					/>
				</td>
				<td>
					{#if v.based_on != null}
						<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- diffBase comes from resolve() -->
						<a href="{diffBase}?from={v.based_on}&to={v.version}">vs v{v.based_on}</a>
					{:else}-{/if}
				</td>
			{/snippet}
			{#snippet empty()}<p>Create a draft with hubctl or the admin API.</p>{/snippet}
			{#snippet footer(page)}
				<div class="history-foot">
					<p class="meta" role="status">
						{page.items.length}
						{page.items.length === 1 ? 'version' : 'versions'}.
						{#if page.next_cursor}Showing the newest {page.items.length}; older versions are not
							loaded.{/if}
					</p>
					{#if page.items.length > 1}
						<button type="submit" class="compare">Compare selected versions</button>
					{/if}
				</div>
			{/snippet}
		</ListView>
	</form>
</section>

{#if current && current.active_version != null && rollbackTo != null}
	{@const active = current.active_version}
	<ConfirmDialog
		bind:open={rollbackOpen}
		title="Roll back {id} to v{rollbackTo}"
		verb="Roll back"
		busyVerb="Rolling back..."
		action="?/rollback"
		subject={id}
		onsuccess={onRolledBack}
	>
		<p>
			Creates v{(current.latest_version ?? active) + 1} with the content of v{rollbackTo} and publishes
			it, replacing the active v{active}. Nothing is deleted: v{active} and every other version stay in
			the history.
		</p>
		<p>
			The full publish validation runs again (an account may have been retired since). Gateway nodes
			switch within about 10 s; queued requests re-resolve to the new version unless they are
			pinned.
		</p>
		<p>
			<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- diffBase comes from resolve() -->
			<a href="{diffBase}?from={active}&to={rollbackTo}"
				>Review what changes (v{active} to v{rollbackTo})</a
			>
		</p>
		{#snippet fields()}
			<input type="hidden" name="to_version" value={rollbackTo} />
			<input type="hidden" name="expected_active_version" value={active} />
		{/snippet}
	</ConfirmDialog>
{/if}

{#if current?.active}
	<WeightsDialog bind:open={weightsOpen} integration={current} onsuccess={onWeights} />
{/if}

{#if current}
	{#if current.paused}
		<ConfirmDialog
			bind:open={pauseOpen}
			title="Resume {id}"
			verb="Resume"
			busyVerb="Resuming..."
			action="?/resume"
			tone="primary"
			subject={id}
			onsuccess={onPauseSwitched}
		>
			<p>
				Clears the kill switch{current.paused_reason ? ` set for: "${current.paused_reason}"` : ''}.
				Within about 10 s, sync and wait calls are served again; queued requests that workers held
				resume on their normal schedule.
			</p>
		</ConfirmDialog>
	{:else}
		<ConfirmDialog
			bind:open={pauseOpen}
			title="Pause {id}"
			verb="Pause"
			busyVerb="Pausing..."
			action="?/pause"
			subject={id}
			onsuccess={onPauseSwitched}
		>
			<p>
				A kill switch for this integration only. Within about 10 s, every sync and wait call to {id}
				gets 503 <code>INTEGRATION_PAUSED</code>, nothing new is parked, and workers hold queued
				requests until you resume.
			</p>
			<p>
				Other integrations on the same accounts are not affected. Pausing is not a version: the
				active v{current.active_version ?? '-'} stays as it is.
			</p>
		</ConfirmDialog>
	{/if}
{/if}

<style>
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
	/* Narrow screens: the groups stack, so a divider on the left would dangle (HUB-287). */
	@media (max-width: 640px) {
		.group.danger {
			padding-left: 0;
			border-left: 0;
		}
	}
	.actions a,
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
		text-decoration: none;
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
	.row-action {
		padding: 0.15rem 0.6rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-xs);
		font-weight: 700;
		cursor: pointer;
	}
	.row-action:hover {
		border-color: var(--signal);
	}
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
	.desc {
		margin: 0;
		color: var(--ink-muted);
	}
	.paused {
		margin: 0;
		padding: var(--space-2) var(--space-3);
		border-left: 4px solid var(--status-neutral);
		background: var(--status-neutral-wash);
		font-size: var(--text-sm);
	}
	.readonly {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.block {
		margin-top: var(--space-5);
		max-width: 72rem;
	}
	.block h2 {
		font-size: var(--text-lg);
	}
	.meta {
		margin: var(--space-1) 0 var(--space-3);
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.active a {
		font-weight: 700;
	}
	.pick {
		text-align: center;
	}
	.pick input {
		accent-color: var(--signal);
	}
	.history-foot {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
		margin-top: var(--space-2);
	}
	.history-foot .meta {
		margin: 0;
	}
	.compare {
		padding: 0.35rem 0.9rem;
		border: 1px solid var(--signal);
		border-radius: var(--radius-sm);
		background: var(--surface);
		color: var(--signal);
		font-weight: 700;
		cursor: pointer;
	}
	.compare:hover {
		background: var(--signal-wash);
	}
	.reason {
		max-width: 22rem;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>

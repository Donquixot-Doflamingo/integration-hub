<script lang="ts">
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { ApiResult } from '$lib/api/client';
	import type { ListPage } from '$lib/api/list';
	import type {
		ApiKey,
		ApiKeyChange,
		ApiKeyRotateResult,
		ApiKeySecretChange,
		ApiKeyState
	} from '$lib/api/types';
	import { can, type Access } from '$lib/auth/roles';
	import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import ListView from '$lib/components/ListView.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import SecretOnce from '$lib/components/SecretOnce.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { formatDurationMs, formatUtc } from '$lib/utils/format';
	import {
		BASE_SCOPES,
		DEFAULT_OVERLAP_MS,
		isLive,
		keyProblems,
		OVERLAPS,
		overlapNote,
		scopeLabels,
		STATES,
		stateLabels,
		keyName,
		type KeyFormValues
	} from './keys';

	/**
	 * API keys of one tenant (HUB-271): metadata only (prefix, scopes, state, dates), never a secret.
	 * Admins create, rotate (with overlap) and revoke. A new key's secret comes back once, in the
	 * create / rotate answer, and is shown once in `SecretOnce`; Done drops it (`reveal = null`).
	 * Nothing else keeps it: `ConfirmDialog` runs with `oneTimeSecret` (not applied as page `form`),
	 * the success notice gets only the change and request ids, and nothing is logged or stored.
	 */
	let {
		tenantId,
		access,
		keys,
		keyState,
		onnotice
	}: {
		tenantId: string;
		access: Access;
		keys: Promise<ApiResult<ListPage<ApiKey>>>;
		keyState: ApiKeyState | undefined;
		onnotice: (n: { message: string; changeId: string | null; requestId: string | null }) => void;
	} = $props();

	const id = $props.id();
	// HUB-288: row actions only for roles that may call them (contract x-required-role).
	const canRotate = $derived(can(access, 'rotateApiKey'));
	const canRevoke = $derived(can(access, 'revokeApiKey'));
	const here = $derived(resolve('/(app)/tenants/[tenant_id]', { tenant_id: tenantId }));

	function onFilter(e: Event) {
		const v = (e.currentTarget as HTMLSelectElement).value;
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- `here` comes from resolve()
		goto(v ? `${here}?key_state=${v}` : here, { keepFocus: true, noScroll: true });
	}

	// The one place a new secret lives, between the action answer and Done.
	let reveal = $state.raw<{
		title: string;
		label: string;
		secret: string;
		notes: string[];
	} | null>(null);

	// Create.
	let createOpen = $state(false);
	let values = $state<KeyFormValues>({
		scopes: ['invoke'],
		integration: '',
		description: '',
		expires: ''
	});
	let touched = $state(false);
	$effect.pre(() => {
		if (createOpen) {
			values = { scopes: ['invoke'], integration: '', description: '', expires: '' };
			touched = false;
		}
	});
	const problems = $derived(keyProblems(values));

	function onCreated(d: ActionSuccessData) {
		const r = d.result as ApiKeySecretChange;
		reveal = {
			title: `Copy the new API key for ${tenantId}`,
			label: `New API key ${keyName(r.api_key)} (scopes ${r.api_key.scopes.join(', ')})`,
			secret: r.secret,
			notes: []
		};
		onnotice({
			message: `Created API key ${keyName(r.api_key)} for ${tenantId} (${r.api_key.scopes.join(', ')}).`,
			changeId: d.changeId,
			requestId: d.requestId
		});
	}

	// Rotate / revoke the picked key.
	let target = $state.raw<ApiKey | null>(null);
	let rotateOpen = $state(false);
	let revokeOpen = $state(false);
	let overlap = $state(DEFAULT_OVERLAP_MS);
	function openRotate(k: ApiKey) {
		target = k;
		overlap = DEFAULT_OVERLAP_MS;
		rotateOpen = true;
	}
	function openRevoke(k: ApiKey) {
		target = k;
		revokeOpen = true;
	}
	function onRotated(d: ActionSuccessData) {
		const r = d.result as ApiKeyRotateResult;
		const note = overlapNote(r.old_key, overlap);
		reveal = {
			title: `Copy the replacement API key for ${tenantId}`,
			label: `New API key ${keyName(r.api_key)} (replaces ${keyName(r.old_key)})`,
			secret: r.secret,
			notes: [note]
		};
		onnotice({
			message: `Rotated ${keyName(r.old_key)} to ${keyName(r.api_key)}. ${note}`,
			changeId: d.changeId,
			requestId: d.requestId
		});
	}
	function onRevoked(d: ActionSuccessData) {
		const k = (d.result as ApiKeyChange).api_key;
		onnotice({
			message: `Revoked API key ${keyName(k)}. Within about 10 s, calls with it get 401.`,
			changeId: d.changeId,
			requestId: d.requestId
		});
	}

	const badge = (s: ApiKeyState) =>
		s === 'active'
			? ({ tone: 'ok', icon: 'closed' } as const)
			: s === 'expiring'
				? ({ tone: 'warn', icon: 'halfOpen' } as const)
				: ({ tone: 'neutral', icon: 'close' } as const);
</script>

<section class="block" aria-labelledby="{id}-heading">
	<div class="title-row">
		<h2 id="{id}-heading">API keys</h2>
		<RoleGate {access} op="createApiKey">
			<button type="button" class="primary" onclick={() => (createOpen = true)}>
				Create API key
			</button>
		</RoleGate>
	</div>
	<p class="meta">
		Callers send a key as a bearer token. Only its first characters (the prefix) are kept for
		reference; the full key is shown once, when it is created or rotated.
	</p>
	<div class="filter">
		<label for="{id}-state">State</label>
		<select id="{id}-state" value={keyState ?? ''} onchange={onFilter}>
			<option value="">Any</option>
			{#each STATES as s (s)}<option value={s}>{stateLabels[s]}</option>{/each}
		</select>
	</div>
	<ListView
		result={keys}
		noun="API keys"
		caption="API keys of {tenantId}"
		emptyTitle={keyState ? `No ${stateLabels[keyState].toLowerCase()} API keys` : 'No API keys yet'}
		filtered={!!keyState}
		clearHref={here}
		columns={[
			{ label: 'Prefix' },
			{ label: 'State' },
			...(canRotate || canRevoke ? [{ label: 'Actions' }] : []),
			{ label: 'Description' },
			{ label: 'Scopes' },
			{ label: 'Created' },
			{ label: 'Expires' },
			{ label: 'Last used' },
			{ label: 'Replaced by' }
		]}
		onretry={invalidateAll}
	>
		{#snippet row(k)}
			{@const b = badge(k.state)}
			<th scope="row">
				<code class="id" title={k.prefix.trim() ? undefined : 'No prefix recorded; key id shown'}
					>{keyName(k)}</code
				>
			</th>
			<td><StatusBadge tone={b.tone} icon={b.icon} label={stateLabels[k.state]} /></td>
			{#if canRotate || canRevoke}
				<td class="row-actions">
					{#if k.state === 'active' && canRotate}
						<button
							type="button"
							class="row-action"
							aria-label="Rotate {keyName(k)}"
							onclick={() => openRotate(k)}>Rotate</button
						>
					{/if}
					{#if isLive(k) && canRevoke}
						<button
							type="button"
							class="row-action stop"
							aria-label="Revoke {keyName(k)}"
							onclick={() => openRevoke(k)}>Revoke</button
						>
					{:else if !isLive(k)}<span class="muted">-</span>{/if}
				</td>
			{/if}
			<td>{k.description ?? '-'}</td>
			<td
				>{#each k.scopes as s (s)}<code class="scope">{s}</code>{/each}</td
			>
			<td>{formatUtc(k.created_at)}</td>
			<td>{k.expires_at ? formatUtc(k.expires_at) : 'Never'}</td>
			<td>{k.last_used_at ? formatUtc(k.last_used_at) : 'Never'}</td>
			<td
				>{#if k.replaced_by}<code class="id">{k.replaced_by}</code>{:else}-{/if}</td
			>
		{/snippet}
		{#snippet empty()}
			<p>
				{keyState
					? 'Pick another state or show all keys.'
					: 'Create a key for each calling service, with only the scopes it needs.'}
			</p>
		{/snippet}
	</ListView>
</section>

<ConfirmDialog
	bind:open={createOpen}
	title="Create an API key for {tenantId}"
	verb="Create key"
	busyVerb="Creating..."
	action="?/createKey"
	tone="primary"
	subject={tenantId}
	ready={Object.keys(problems).length === 0}
	oneTimeSecret
	onsuccess={onCreated}
>
	<p>
		The key is shown once, right after it is created. Callers can use it within about 10 s, for the
		scopes you pick.
	</p>
	{#snippet fields(errors)}
		{@const scopesError = errors.scopes ?? (touched ? problems.scopes : null)}
		<fieldset class="scopes" aria-describedby={scopesError ? `${id}-scopes-error` : undefined}>
			<legend>Scopes <span class="req">(at least one)</span></legend>
			{#each BASE_SCOPES as s (s)}
				<label class="check">
					<input
						type="checkbox"
						name="scope"
						value={s}
						bind:group={values.scopes}
						onchange={() => (touched = true)}
					/>
					{scopeLabels[s]}
				</label>
			{/each}
			{#if scopesError}
				<p id="{id}-scopes-error" class="field-error">
					<Icon name="alert" size={14} />{scopesError}
				</p>
			{/if}
		</fieldset>
		<div class="field">
			<label for="{id}-integration">Limit to one integration</label>
			<input
				id="{id}-integration"
				name="integration"
				bind:value={values.integration}
				autocomplete="off"
				spellcheck="false"
				placeholder="int_evt_brandA"
				aria-invalid={problems.integration ? 'true' : undefined}
				aria-describedby="{id}-integration-hint{problems.integration
					? ` ${id}-integration-error`
					: ''}"
			/>
			<p id="{id}-integration-hint" class="hint">
				Optional. Narrows every scope above to this integration (<code>invoke:int_evt_brandA</code
				>).
			</p>
			{#if problems.integration}
				<p id="{id}-integration-error" class="field-error">
					<Icon name="alert" size={14} />{problems.integration}
				</p>
			{/if}
		</div>
		<div class="field">
			<label for="{id}-description">Description</label>
			<input
				id="{id}-description"
				name="description"
				bind:value={values.description}
				autocomplete="off"
				placeholder="orders prod"
			/>
		</div>
		<div class="field">
			<label for="{id}-expires">Expires (UTC)</label>
			<input
				id="{id}-expires"
				name="expires_at"
				type="datetime-local"
				bind:value={values.expires}
				aria-invalid={(errors.expires_at ?? problems.expires_at) ? 'true' : undefined}
				aria-describedby="{id}-expires-hint{(errors.expires_at ?? problems.expires_at)
					? ` ${id}-expires-error`
					: ''}"
			/>
			<p id="{id}-expires-hint" class="hint">Optional. Empty means the key does not expire.</p>
			{#if errors.expires_at ?? problems.expires_at}
				<p id="{id}-expires-error" class="field-error">
					<Icon name="alert" size={14} />{errors.expires_at ?? problems.expires_at}
				</p>
			{/if}
		</div>
	{/snippet}
</ConfirmDialog>

{#if target}
	{@const k = target}
	<ConfirmDialog
		bind:open={rotateOpen}
		title="Rotate API key {keyName(k)}"
		verb="Rotate"
		busyVerb="Rotating..."
		action="?/rotateKey"
		tone="primary"
		subject="API key {keyName(k)}"
		oneTimeSecret
		onsuccess={onRotated}
	>
		<p>
			Mints a replacement with the same tenant and scopes ({k.scopes.join(', ')}). The new key is
			shown once. The old key {keyName(k)} keeps working for the overlap you pick (state Expiring), then
			it is revoked automatically.
		</p>
		{#snippet fields()}
			<input type="hidden" name="key_id" value={k.key_id} />
			<div class="field">
				<label for="{id}-overlap">Overlap</label>
				<select id="{id}-overlap" name="overlap_ms" bind:value={overlap}>
					{#each OVERLAPS as o (o.ms)}<option value={o.ms}>{o.label}</option>{/each}
				</select>
				<p class="hint">
					{overlap === 0
						? `${keyName(k)} stops working as soon as the new key exists.`
						: `${keyName(k)} keeps working for ${formatDurationMs(overlap)} after the rotation.`}
				</p>
			</div>
		{/snippet}
	</ConfirmDialog>

	<ConfirmDialog
		bind:open={revokeOpen}
		title="Revoke API key {keyName(k)}"
		verb="Revoke"
		busyVerb="Revoking..."
		action="?/revokeKey"
		confirmText={keyName(k)}
		subject="API key {keyName(k)}"
		onsuccess={onRevoked}
	>
		<p>
			Within about 10 s, every call with {keyName(k)} ({k.description ?? 'no description'}) gets
			401. Revoking cannot be undone: create or rotate a key to give the caller access again.
		</p>
		{#snippet fields()}
			<input type="hidden" name="key_id" value={k.key_id} />
			<input type="hidden" name="prefix" value={keyName(k)} />
		{/snippet}
	</ConfirmDialog>
{/if}

{#if reveal}
	<SecretOnce
		title={reveal.title}
		label={reveal.label}
		secret={reveal.secret}
		notes={reveal.notes}
		ondone={() => (reveal = null)}
	/>
{/if}

<style>
	.block {
		margin-top: var(--space-5);
		max-width: 72rem;
	}
	.title-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
	}
	h2 {
		font-size: var(--text-lg);
	}
	.meta {
		margin: var(--space-1) 0 var(--space-3);
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.filter {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		margin-bottom: var(--space-2);
		font-size: var(--text-sm);
	}
	.filter label {
		font-weight: 600;
		color: var(--ink-muted);
	}
	.filter select,
	.field select {
		height: 2rem;
		padding: 0 0.5rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-sm);
	}
	.primary {
		min-height: 2.125rem;
		padding: 0 0.9rem;
		border: 1px solid var(--signal);
		border-radius: var(--radius-sm);
		background: var(--signal);
		color: var(--surface);
		font-weight: 700;
		cursor: pointer;
	}
	.row-actions {
		display: flex;
		gap: var(--space-2);
		align-items: center;
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
	.row-action.stop {
		border-color: var(--status-danger);
		color: var(--status-danger);
	}
	.scope + .scope {
		margin-left: var(--space-1);
	}
	fieldset {
		margin: 0;
		padding: var(--space-2) var(--space-3) var(--space-3);
		border: 1px solid var(--rule);
		border-radius: var(--radius-sm);
		display: grid;
		gap: var(--space-1);
	}
	legend,
	.field > label {
		font-weight: 600;
		font-size: var(--text-sm);
	}
	.req {
		color: var(--ink-muted);
		font-weight: 400;
	}
	.check {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		font-size: var(--text-sm);
	}
	.field {
		display: grid;
		gap: 0.25rem;
	}
	.field input {
		width: 100%;
		padding: 0.4rem 0.55rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-sm);
	}
	[aria-invalid='true'] {
		border-color: var(--status-danger);
	}
	.hint {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.field-error {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		margin: 0;
		color: var(--status-danger);
		font-size: var(--text-sm);
		font-weight: 600;
	}
</style>

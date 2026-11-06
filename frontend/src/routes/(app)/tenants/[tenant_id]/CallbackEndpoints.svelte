<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import type { ActionSuccessData, FieldErrors } from '$lib/api/actions';
	import type { ApiResult } from '$lib/api/client';
	import type { ListPage } from '$lib/api/list';
	import type {
		RegistrarEndpoint,
		RegistrarEndpointChange,
		RegistrarEndpointSecretChange
	} from '$lib/api/types';
	import { can, type Access } from '$lib/auth/roles';
	import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import ListView from '$lib/components/ListView.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import SecretOnce from '$lib/components/SecretOnce.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { formatDurationMs, formatNumber, formatUtc } from '$lib/utils/format';
	import { BEFORE_PREFIX } from '../tenant-form';
	import {
		endpointProblems,
		SECRET_OVERLAPS,
		secretText,
		SIGNING_SECRET_WARNING,
		updateEndpointBody,
		valuesFromEndpoint,
		type EndpointValues
	} from './endpoints';
	import { DEFAULT_OVERLAP_MS } from './keys';

	/**
	 * Registrar (callback) endpoints of one tenant (HUB-272): where the Hub delivers async results
	 * and webhooks. Signing secrets are write-only: the list shows only the secret version (and the
	 * overlap while two secrets sign). A new secret (register, rotate) is shown exactly once in
	 * `SecretOnce`, with the same rules as API keys (HUB-271): `oneTimeSecret` dialogs, enhanced-only
	 * actions, dropped on Done, never stored or logged. Admins register, edit, disable / enable,
	 * rotate the secret and delete; the SSRF guard's error lands on the URL field.
	 */
	let {
		tenantId,
		access,
		endpoints,
		onnotice
	}: {
		tenantId: string;
		access: Access;
		endpoints: Promise<ApiResult<ListPage<RegistrarEndpoint>>>;
		onnotice: (n: { message: string; changeId: string | null; requestId: string | null }) => void;
	} = $props();

	const id = $props.id();
	// HUB-288: each row action only for roles that may call its operation (contract x-required-role).
	const canUpdate = $derived(can(access, 'updateRegistrarEndpoint'));
	const canRotate = $derived(can(access, 'rotateRegistrarSecret'));
	const canDelete = $derived(can(access, 'deleteRegistrarEndpoint'));
	const anyRowAction = $derived(canUpdate || canRotate || canDelete);
	const EMPTY: EndpointValues = { endpoint_id: '', url: '', description: '' };
	// The one place a new signing secret lives, between the action answer and Done.
	let reveal = $state.raw<{
		title: string;
		label: string;
		secret: string;
		notes: string[];
	} | null>(null);

	function revealSecret(d: ActionSuccessData, title: string, notes: string[] = []) {
		const r = d.result as RegistrarEndpointSecretChange;
		reveal = {
			title,
			label: `Signing secret version ${r.endpoint.secret_version} of ${r.endpoint.endpoint_id}`,
			secret: r.signing_secret,
			notes
		};
	}

	// Register.
	let createOpen = $state(false);
	let createValues = $state<EndpointValues>({ ...EMPTY });
	let createTouched = $state<Partial<Record<keyof EndpointValues, boolean>>>({});
	$effect.pre(() => {
		if (createOpen) {
			createValues = { ...EMPTY };
			createTouched = {};
		}
	});
	const createProblems = $derived(endpointProblems(createValues, 'create'));

	// The endpoint a row action picked.
	let target = $state.raw<RegistrarEndpoint | null>(null);
	let editOpen = $state(false);
	let switchOpen = $state(false);
	let rotateOpen = $state(false);
	let deleteOpen = $state(false);
	let editBefore = $state.raw<EndpointValues>(EMPTY);
	let editValues = $state<EndpointValues>({ ...EMPTY });
	let editTouched = $state(false);
	let overlap = $state(DEFAULT_OVERLAP_MS);
	const editProblems = $derived(endpointProblems(editValues, 'edit'));
	const editChanged = $derived(updateEndpointBody(editValues, editBefore, 'x').ok);

	function pick(e: RegistrarEndpoint, open: 'edit' | 'switch' | 'rotate' | 'delete') {
		target = e;
		if (open === 'edit') {
			const before = valuesFromEndpoint(e);
			editBefore = before;
			editValues = { ...before };
			editTouched = false;
			editOpen = true;
		} else if (open === 'switch') switchOpen = true;
		else if (open === 'rotate') {
			overlap = DEFAULT_OVERLAP_MS;
			rotateOpen = true;
		} else deleteOpen = true;
	}

	function changed(message: (e: RegistrarEndpoint) => string) {
		return (d: ActionSuccessData) => {
			const e = (d.result as RegistrarEndpointChange).endpoint;
			onnotice({ message: message(e), changeId: d.changeId, requestId: d.requestId });
		};
	}
	const onCreatedNotice = changed(
		(e) =>
			`Registered callback endpoint ${e.endpoint_id} (${e.url}) with signing secret version ${e.secret_version}.`
	);
	function onCreated(d: ActionSuccessData) {
		const e = (d.result as RegistrarEndpointSecretChange).endpoint;
		revealSecret(d, `Copy the callback signing secret of ${e.endpoint_id}`);
		onCreatedNotice(d);
	}
	const onUpdated = changed((e) => `Saved ${e.endpoint_id}: ${e.url}.`);
	const onSwitched = changed((e) =>
		e.disabled
			? `Disabled ${e.endpoint_id}. The Hub stops delivering callbacks to it.`
			: `Enabled ${e.endpoint_id}. Callbacks are delivered to it again.`
	);
	const overlapText = (e: RegistrarEndpoint) =>
		e.secret_overlap_until
			? `Until ${formatUtc(e.secret_overlap_until)}, callbacks carry both signatures (version ${e.secret_version} and ${e.secret_version - 1}); after that only version ${e.secret_version} signs.`
			: `Only version ${e.secret_version} signs from now on.`;
	const onRotatedNotice = changed(
		(e) =>
			`Rotated the signing secret of ${e.endpoint_id} to version ${e.secret_version}. ${overlapText(e)}`
	);
	function onRotated(d: ActionSuccessData) {
		const e = (d.result as RegistrarEndpointSecretChange).endpoint;
		revealSecret(d, `Copy the new callback signing secret of ${e.endpoint_id}`, [overlapText(e)]);
		onRotatedNotice(d);
	}
	function onDeleted(d: ActionSuccessData) {
		onnotice({
			message: `Deleted callback endpoint ${target?.endpoint_id ?? ''}. Pending deliveries to it are dead-lettered.`,
			changeId: d.changeId,
			requestId: d.requestId
		});
	}

	function fieldError(
		key: keyof EndpointValues,
		api: FieldErrors,
		local: FieldErrors,
		touched: boolean
	): string | null {
		return api[key] ?? (touched ? (local[key] ?? null) : null);
	}
</script>

{#snippet urlField(
	values: EndpointValues,
	error: string | null,
	onblur: () => void,
	prefix: string
)}
	<div class="field">
		<label for="{prefix}-url">Callback URL <span class="req">(required)</span></label>
		<input
			id="{prefix}-url"
			name="url"
			type="url"
			bind:value={values.url}
			{onblur}
			autocomplete="off"
			spellcheck="false"
			placeholder="https://orders.internal/hub/callbacks"
			aria-invalid={error ? 'true' : undefined}
			aria-describedby="{prefix}-url-hint{error ? ` ${prefix}-url-error` : ''}"
		/>
		<p id="{prefix}-url-hint" class="hint">
			https only. The Hub checks the host against its allowlist and refuses private or link-local
			addresses.
		</p>
		{#if error}
			<p id="{prefix}-url-error" class="field-error"><Icon name="alert" size={14} />{error}</p>
		{/if}
	</div>
	<div class="field">
		<label for="{prefix}-description">Description</label>
		<input
			id="{prefix}-description"
			name="description"
			bind:value={values.description}
			autocomplete="off"
		/>
	</div>
{/snippet}

<section class="block" aria-labelledby="{id}-heading">
	<div class="title-row">
		<h2 id="{id}-heading">Callback endpoints</h2>
		<RoleGate {access} op="createRegistrarEndpoint">
			<button type="button" class="primary" onclick={() => (createOpen = true)}>
				Register endpoint
			</button>
		</RoleGate>
	</div>
	<p class="meta">
		Where the Hub delivers this tenant's async results and webhooks (registrar). Each delivery is
		signed with the endpoint's signing secret (<code>X-Hub-Signature</code>); a new secret is shown
		once, when it is created or rotated, and only its version afterwards.
	</p>
	<ListView
		result={endpoints}
		noun="callback endpoints"
		caption="Callback endpoints of {tenantId}"
		emptyTitle="No callback endpoints yet"
		columns={[
			{ label: 'Endpoint' },
			{ label: 'State' },
			...(anyRowAction ? [{ label: 'Actions' }] : []),
			{ label: 'URL' },
			{ label: 'Signing secret' },
			{ label: 'Subscribed versions', numeric: true },
			{ label: 'Updated' }
		]}
		onretry={invalidateAll}
	>
		{#snippet row(e)}
			<th scope="row"><code class="id">{e.endpoint_id}</code></th>
			<td>
				{#if e.disabled}
					<StatusBadge tone="neutral" icon="pause" label="Disabled" />
				{:else}
					<StatusBadge tone="ok" icon="closed" label="Enabled" />
				{/if}
			</td>
			{#if anyRowAction}
				<td class="row-actions">
					{#if canUpdate}
						<button type="button" class="row-action" onclick={() => pick(e, 'edit')}>
							Edit {e.endpoint_id}
						</button>
					{/if}
					{#if canRotate}
						<button type="button" class="row-action" onclick={() => pick(e, 'rotate')}>
							Rotate secret of {e.endpoint_id}
						</button>
					{/if}
					{#if canUpdate}
						<button type="button" class="row-action" onclick={() => pick(e, 'switch')}>
							{e.disabled ? 'Enable' : 'Disable'}
							{e.endpoint_id}
						</button>
					{/if}
					{#if canDelete}
						<button type="button" class="row-action stop" onclick={() => pick(e, 'delete')}>
							Delete {e.endpoint_id}
						</button>
					{/if}
				</td>
			{/if}
			<td>
				<code class="url">{e.url}</code>
				{#if e.description}<span class="desc">{e.description}</span>{/if}
			</td>
			<td>{secretText(e)}</td>
			<td class="numeric">{formatNumber(e.subscribed_by)}</td>
			<td>{formatUtc(e.updated_at)}</td>
		{/snippet}
		{#snippet empty()}
			<p>Register an endpoint before an integration of this tenant uses async delivery.</p>
		{/snippet}
	</ListView>
</section>

<ConfirmDialog
	bind:open={createOpen}
	title="Register a callback endpoint for {tenantId}"
	verb="Register"
	busyVerb="Registering..."
	action="?/createEndpoint"
	tone="primary"
	subject={tenantId}
	ready={Object.keys(createProblems).length === 0}
	oneTimeSecret
	onsuccess={onCreated}
>
	<p>
		The Hub mints a signing secret for it, shown once right after: the tenant's receiver needs it to
		verify <code>X-Hub-Signature</code>. Integrations of {tenantId} can then subscribe to the endpoint.
	</p>
	{#snippet fields(errors)}
		{@const idError = fieldError(
			'endpoint_id',
			errors,
			createProblems,
			!!createTouched.endpoint_id
		)}
		<div class="field">
			<label for="{id}-c-id">Endpoint id <span class="req">(required)</span></label>
			<input
				id="{id}-c-id"
				name="endpoint_id"
				bind:value={createValues.endpoint_id}
				onblur={() => (createTouched.endpoint_id = true)}
				maxlength="64"
				autocomplete="off"
				spellcheck="false"
				placeholder="reg_orders_cb"
				aria-invalid={idError ? 'true' : undefined}
				aria-describedby={idError ? `${id}-c-id-error` : undefined}
			/>
			{#if idError}
				<p id="{id}-c-id-error" class="field-error"><Icon name="alert" size={14} />{idError}</p>
			{/if}
		</div>
		{@render urlField(
			createValues,
			fieldError('url', errors, createProblems, !!createTouched.url),
			() => (createTouched.url = true),
			`${id}-c`
		)}
	{/snippet}
</ConfirmDialog>

{#if target}
	{@const e = target}
	<ConfirmDialog
		bind:open={editOpen}
		title="Edit callback endpoint {e.endpoint_id}"
		verb="Save changes"
		busyVerb="Saving..."
		action="?/updateEndpoint"
		tone="primary"
		subject={e.endpoint_id}
		ready={Object.keys(editProblems).length === 0 && editChanged}
		onsuccess={onUpdated}
	>
		<p>
			A new URL goes through the same SSRF check. Deliveries already queued go to the new URL from
			their next attempt.
		</p>
		{#snippet fields(errors)}
			<input type="hidden" name="endpoint_id" value={e.endpoint_id} />
			<input type="hidden" name="{BEFORE_PREFIX}url" value={editBefore.url} />
			<input type="hidden" name="{BEFORE_PREFIX}description" value={editBefore.description} />
			{@render urlField(
				editValues,
				fieldError('url', errors, editProblems, editTouched),
				() => (editTouched = true),
				`${id}-e`
			)}
			{#if !editChanged && Object.keys(editProblems).length === 0}
				<p class="hint" role="status">Change the URL or the description to save.</p>
			{/if}
		{/snippet}
	</ConfirmDialog>

	<ConfirmDialog
		bind:open={switchOpen}
		title="{e.disabled ? 'Enable' : 'Disable'} callback endpoint {e.endpoint_id}"
		verb={e.disabled ? 'Enable' : 'Disable'}
		busyVerb={e.disabled ? 'Enabling...' : 'Disabling...'}
		action="?/setEndpointDisabled"
		tone={e.disabled ? 'primary' : 'danger'}
		subject={e.endpoint_id}
		onsuccess={onSwitched}
	>
		<p>
			{e.disabled
				? `The Hub delivers callbacks to ${e.url} again.`
				: `The Hub stops delivering callbacks to ${e.url}. ${e.subscribed_by} integration ${e.subscribed_by === 1 ? 'version subscribes' : 'versions subscribe'} to it.`}
		</p>
		{#snippet fields()}
			<input type="hidden" name="endpoint_id" value={e.endpoint_id} />
			<input type="hidden" name="disabled" value={e.disabled ? 'false' : 'true'} />
		{/snippet}
	</ConfirmDialog>

	<ConfirmDialog
		bind:open={rotateOpen}
		title="Rotate the signing secret of {e.endpoint_id}"
		verb="Rotate secret"
		busyVerb="Rotating..."
		action="?/rotateSecret"
		tone="primary"
		subject={e.endpoint_id}
		oneTimeSecret
		onsuccess={onRotated}
	>
		<p>
			Mints signing secret version {e.secret_version + 1}, shown once right after. During the
			overlap, callbacks carry two <code>v1=</code> signatures (new and old), so the receiver can switch
			without dropping any.
		</p>
		{#snippet fields()}
			<input type="hidden" name="endpoint_id" value={e.endpoint_id} />
			<div class="field">
				<label for="{id}-overlap">Overlap</label>
				<select id="{id}-overlap" name="overlap_ms" bind:value={overlap}>
					{#each SECRET_OVERLAPS as o (o.ms)}<option value={o.ms}>{o.label}</option>{/each}
				</select>
				<p class="hint">
					{overlap === 0
						? 'Only the new secret signs from the moment it exists.'
						: `Both secrets sign for ${formatDurationMs(overlap)}.`}
				</p>
			</div>
		{/snippet}
	</ConfirmDialog>

	<ConfirmDialog
		bind:open={deleteOpen}
		title="Delete callback endpoint {e.endpoint_id}"
		verb="Delete"
		busyVerb="Deleting..."
		action="?/deleteEndpoint"
		confirmText={e.endpoint_id}
		subject={e.endpoint_id}
		onsuccess={onDeleted}
	>
		<p>
			Pending deliveries to {e.url} are dead-lettered. This cannot be undone.
			{#if e.subscribed_by > 0}
				<strong
					>{e.subscribed_by} active or draft integration {e.subscribed_by === 1
						? 'version subscribes'
						: 'versions subscribe'} to it: the Hub refuses the delete until none does.</strong
				>
			{/if}
		</p>
		{#snippet fields()}
			<input type="hidden" name="endpoint_id" value={e.endpoint_id} />
		{/snippet}
	</ConfirmDialog>
{/if}

{#if reveal}
	<SecretOnce
		title={reveal.title}
		label={reveal.label}
		secret={reveal.secret}
		notes={reveal.notes}
		warning={SIGNING_SECRET_WARNING}
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
	.desc {
		display: block;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.field {
		display: grid;
		gap: 0.25rem;
	}
	.field > label {
		font-weight: 600;
		font-size: var(--text-sm);
	}
	.req {
		color: var(--ink-muted);
		font-weight: 400;
	}
	.field input,
	.field select {
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

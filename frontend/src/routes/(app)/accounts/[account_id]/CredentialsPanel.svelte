<script lang="ts">
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { Account, ConnectionTestResult, CredentialSlotsChange } from '$lib/api/types';
	import { can, type Access } from '$lib/auth/roles';
	import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
	import FieldError from '$lib/components/FieldError.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import type { Flash } from '$lib/stores/flash.svelte';
	import { formatUtc } from '$lib/utils/format';
	import CredentialInputs from '../CredentialInputs.svelte';
	import {
		credentialProblem,
		expiryProblem,
		rotationStep,
		slot,
		slotLabels,
		type CredentialRow
	} from '../account-form';
	import ConnectionTest from './ConnectionTest.svelte';

	/**
	 * Credential slots and the rotation flow (HUB-291, DESIGN §6.6): add the next credential
	 * (admin), test it (editor), promote it (admin), then delete the previous one (admin). Only
	 * metadata is ever shown (version, fingerprint, dates); secret values are write-only, typed in
	 * the "Add next credential" dialog and cleared as soon as it is submitted. Each step renders
	 * only for roles whose `x-required-role` allows it.
	 */
	let {
		account,
		endpoints,
		access,
		noCredentials = false,
		onnotice
	}: {
		account: Account;
		/** The vendor's auth scheme is `none` (HUB-436): no credentials, nothing to rotate. */
		noCredentials?: boolean;
		endpoints: string[];
		access: Access;
		onnotice: (n: Flash) => void;
	} = $props();

	const id = $derived(account.account_id);
	const slots = $derived(account.credentials);
	const active = $derived(slot(slots, 'active'));
	const next = $derived(slot(slots, 'next'));
	const previous = $derived(slot(slots, 'previous'));
	const retired = $derived(account.state === 'retired');

	/** cred_version of the next credential that passed a connection test on this page. */
	let testedNext = $state<number | null>(null);
	const step = $derived(rotationStep(slots, testedNext));
	function ontested(r: ConnectionTestResult) {
		if (r.slot === 'next') testedNext = r.ok ? r.cred_version : null;
	}

	let addOpen = $state(false);
	let promoteOpen = $state(false);
	let deleteOpen = $state(false);
	let rows = $state<CredentialRow[]>([{ key: '', value: '' }]);
	let expires = $state('');
	let cleared = $state(false);
	// A fresh form each time the dialog opens; the keys of the active credential are not known
	// (only metadata is returned), so the operator types them.
	$effect.pre(() => {
		if (addOpen) {
			rows = [{ key: '', value: '' }];
			expires = '';
			cleared = false;
		}
	});
	function forgetSecrets() {
		for (const r of rows) r.value = '';
		cleared = true;
	}
	const addReady = $derived(!credentialProblem(rows) && !expiryProblem(expires));

	function slotsNotice(verb: string) {
		return (d: ActionSuccessData) => {
			const c = d.result as CredentialSlotsChange;
			const now = c.credentials.find((s) => s.slot === 'active');
			const waiting = c.credentials.find((s) => s.slot === 'next');
			onnotice({
				message: `${verb} Active credential: v${now?.cred_version ?? '-'}${waiting ? `; next: v${waiting.cred_version} (not used until promoted)` : ''}.`,
				changeId: d.changeId,
				requestId: d.requestId
			});
		};
	}
	const onAdded = $derived(
		slotsNotice(`Stored the next credential for ${id}. Test it, then promote it.`)
	);
	const onPromoted = $derived(
		slotsNotice(
			`Promoted the next credential of ${id}. Every new attempt uses it; keep the previous one until no UPSTREAM_AUTH_FAILED shows for 15 min.`
		)
	);
	const onDeleted = $derived(slotsNotice(`Deleted the previous credential of ${id}.`));

	const steps = [
		{ key: 'add', label: 'Add the next credential' },
		{ key: 'test', label: 'Test it' },
		{ key: 'promote', label: 'Promote it to active' },
		{ key: 'delete', label: 'Delete the previous one after the watch' }
	] as const;
	const order = { add: 0, test: 1, promote: 2, delete: 3, done: 4 } as const;
	const stateOf = (k: (typeof steps)[number]['key']) =>
		order[k] < order[step] ? 'Done' : order[k] === order[step] ? 'Now' : 'Later';
	const testSlots = $derived<('active' | 'next')[]>([
		...(active ? (['active'] as const) : []),
		...(next ? (['next'] as const) : [])
	]);
</script>

<section class="block" aria-labelledby="creds-h">
	<h2 id="creds-h">Credentials</h2>
	<p class="fk-note">
		Secret values are write-only: the Hub stores them encrypted and returns only version,
		fingerprint and dates.
	</p>
	{#if noCredentials && slots.length === 0}
		<p class="empty" role="status">
			No credentials: the vendor is a public API (auth scheme none), so there is nothing to store or
			rotate.
		</p>
	{:else if slots.length === 0}
		<p class="empty" role="status">
			No credentials: {retired ? 'retiring deleted them.' : 'add one below.'}
		</p>
	{:else}
		<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
		<div class="table-scroll" role="region" aria-label="Credential slots" tabindex="0">
			<table>
				<thead>
					<tr>
						<th scope="col">Slot</th>
						<th scope="col">Version</th>
						<th scope="col">Fingerprint</th>
						<th scope="col">Stored</th>
						<th scope="col">Expires</th>
					</tr>
				</thead>
				<tbody>
					{#each slots as s (s.slot)}
						<tr>
							<th scope="row">{slotLabels[s.slot]}</th>
							<td>v{s.cred_version}</td>
							<td><code>{s.fingerprint}</code></td>
							<td>{formatUtc(s.created_at)}</td>
							<td>{s.expires_at ? formatUtc(s.expires_at) : 'Never'}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}

	{#if !retired && !noCredentials}
		<h3>Rotate</h3>
		<ol class="steps" aria-label="Rotation steps">
			{#each steps as s (s.key)}
				<li class={stateOf(s.key).toLowerCase()}>
					<span class="step-state">{stateOf(s.key)}</span>
					{s.label}
				</li>
			{/each}
		</ol>

		<div class="step-actions">
			<RoleGate {access} op="addNextCredential">
				<button
					type="button"
					class="btn"
					class:btn-primary={step === 'add'}
					onclick={() => (addOpen = true)}
				>
					{next ? `Replace next credential v${next.cred_version}` : 'Add next credential'}
				</button>
			</RoleGate>
			{#if next}
				<RoleGate {access} op="promoteNextCredential">
					<button
						type="button"
						class="btn"
						class:btn-primary={step === 'promote'}
						onclick={() => (promoteOpen = true)}>Promote v{next.cred_version}</button
					>
				</RoleGate>
			{/if}
			{#if previous}
				<RoleGate {access} op="deleteOldCredential">
					<button type="button" class="btn btn-danger" onclick={() => (deleteOpen = true)}>
						Delete previous credential v{previous.cred_version}
					</button>
				</RoleGate>
			{/if}
		</div>
		{#if !can(access, 'addNextCredential')}
			<p class="fk-note" role="note">Rotating credentials needs the Admin role.</p>
		{/if}

		{#if testSlots.length}
			<RoleGate {access} op="testAccountConnection">
				<h3>Test connection</h3>
				<ConnectionTest
					accountId={id}
					slots={testSlots}
					{endpoints}
					defaultSlot={next ? 'next' : 'active'}
					{ontested}
				/>
				{#snippet denied(role)}
					<p class="fk-note" role="note">Testing the connection needs the {role} role.</p>
				{/snippet}
			</RoleGate>
		{/if}
	{/if}
</section>

<ConfirmDialog
	bind:open={addOpen}
	title="Add the next credential to {id}"
	verb="Store next credential"
	busyVerb="Storing..."
	action="?/addNext"
	tone="primary"
	subject={id}
	conflictIsStale={false}
	ready={addReady}
	onsubmitted={forgetSecrets}
	onsuccess={onAdded}
>
	<p>
		Stored in the <strong>next</strong> slot{next
			? `, replacing the un-promoted v${next.cred_version}`
			: ''}. Calls keep using the active credential{active ? ` v${active.cred_version}` : ''} until you
		promote it. Values are never shown again.
	</p>
	{#snippet fields(errors)}
		<CredentialInputs bind:rows legend="New credential" error={errors['/credentials'] ?? null} />
		{#if cleared}
			<p class="fk-note" role="status">
				The secret values were cleared when you submitted. Type them again to retry.
			</p>
		{/if}
		<div class="fk-field">
			<label for="{id}-next-exp">Expires (UTC)</label>
			<input
				id="{id}-next-exp"
				name="expires_at"
				type="datetime-local"
				bind:value={expires}
				aria-invalid={errors['/expires_at'] || expiryProblem(expires) ? 'true' : undefined}
				aria-describedby="{id}-next-exp-error"
			/>
			<FieldError
				id="{id}-next-exp-error"
				message={errors['/expires_at'] ?? expiryProblem(expires)}
			/>
		</div>
	{/snippet}
</ConfirmDialog>

{#if next}
	<ConfirmDialog
		bind:open={promoteOpen}
		title="Promote credential v{next.cred_version} of {id}"
		verb="Promote v{next.cred_version}"
		busyVerb="Promoting..."
		action="?/promote"
		tone="primary"
		subject={id}
		onsuccess={onPromoted}
	>
		<p>
			v{next.cred_version} becomes active for every later attempt, including parked requests drained afterwards.{active
				? ` v${active.cred_version} moves to the previous slot: kept, not used, so you can go back by rotating again.`
				: ''}
		</p>
		{#if testedNext !== next.cred_version}
			<p class="warn" role="note">
				v{next.cred_version} has not passed a connection test on this page. Test it first unless you know
				it works.
			</p>
		{/if}
		{#snippet fields()}
			<input type="hidden" name="expected_next_version" value={next?.cred_version ?? ''} />
		{/snippet}
	</ConfirmDialog>
{/if}

{#if previous}
	<ConfirmDialog
		bind:open={deleteOpen}
		title="Delete credential v{previous.cred_version} of {id}"
		verb="Delete v{previous.cred_version}"
		busyVerb="Deleting..."
		action="?/deleteOld"
		confirmText={id}
		subject={id}
		onsuccess={onDeleted}
	>
		<p>
			Destroys the previous credential v{previous.cred_version} for good. After this there is no way back
			to it: do it once the new credential has run for 15 minutes without UPSTREAM_AUTH_FAILED.
		</p>
		{#snippet fields()}
			<input type="hidden" name="expected_previous_version" value={previous?.cred_version ?? ''} />
		{/snippet}
	</ConfirmDialog>
{/if}

<style>
	.block {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: var(--space-2);
		margin-top: var(--space-5);
		max-width: 72rem;
	}
	h2 {
		font-size: var(--text-lg);
	}
	h3 {
		margin-top: var(--space-3);
		font-size: var(--text-md);
	}
	.empty {
		margin: 0;
		padding: var(--space-3);
		border: 1px dashed var(--rule-strong);
		border-radius: var(--radius-md);
		font-size: var(--text-sm);
	}
	.table-scroll {
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
	.steps {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr));
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
		counter-reset: step;
	}
	.steps li {
		counter-increment: step;
		display: grid;
		gap: 0.1rem;
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--rule);
		border-top: 3px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-sm);
	}
	.steps li::before {
		content: counter(step) '.';
		color: var(--ink-muted);
		font-weight: 700;
	}
	.steps li.now {
		border-top-color: var(--signal);
		background: var(--signal-wash);
		font-weight: 700;
	}
	.steps li.done {
		border-top-color: var(--status-ok);
		color: var(--ink-muted);
	}
	.step-state {
		font-size: var(--text-xs);
		color: var(--ink-muted);
	}
	.step-actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}
	.warn {
		padding: var(--space-2) var(--space-3);
		border-left: 4px solid var(--status-warn);
		background: var(--status-warn-wash);
	}
</style>

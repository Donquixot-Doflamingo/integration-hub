<script lang="ts">
	import { untrack } from 'svelte';
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { AccountChange } from '$lib/api/types';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import AuthoringForm from '$lib/components/AuthoringForm.svelte';
	import FieldError from '$lib/components/FieldError.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import { setFlash } from '$lib/stores/flash.svelte';
	import { blockersFrom } from '$lib/utils/form';
	import type { PageData } from './$types';
	import CredentialInputs from '../CredentialInputs.svelte';
	import { emptyValues, problems } from '../account-form';

	/**
	 * Create an account with its first credentials (HUB-291, `createAccount`, admin). The secret
	 * values are cleared from the page as soon as the form is submitted, whatever the outcome; on
	 * an error they must be typed again.
	 */
	let { data }: { data: PageData } = $props();
	const uid = $props.id();

	let values = $state({ ...emptyValues(), vendor_id: untrack(() => data.vendorId) });
	let cleared = $state(false);
	const blockers = $derived(blockersFrom(problems(values)));

	function forgetSecrets() {
		for (const r of values.credentials) r.value = '';
		cleared = true;
	}

	async function onsuccess(d: ActionSuccessData) {
		const a = (d.result as AccountChange).account;
		setFlash({
			message: `Created account ${a.account_id} (${a.vendor_id}, region ${a.region}) with credential v${a.credentials[0]?.cred_version ?? 1}. Test the connection before an integration targets it.`,
			changeId: d.changeId,
			requestId: d.requestId
		});
		await goto(resolve('/(app)/accounts/[account_id]', { account_id: a.account_id }));
	}
	const err = (errors: Record<string, string | undefined>, attempted: boolean, ptr: string) =>
		errors[ptr] ?? (attempted ? (problems(values)[ptr] ?? null) : null);
</script>

<PageHeader
	title="Create account"
	summary="An account is one credentialed workspace at a vendor: its region, non-secret config, quota and secrets."
/>

<RoleGate access={data.access} op="createAccount">
	<AsyncView result={data.vendors} noun="the vendors" onretry={invalidateAll}>
		{#snippet children(page)}
			{@const vendor = page.items.find((v) => v.vendor_id === values.vendor_id)}
			{#if page.items.length === 0}
				<p class="fk-note" role="note">
					No vendors yet. <a href={resolve('/(app)/vendors/new')}>Create the vendor first</a>.
				</p>
			{:else}
				<AuthoringForm
					action="?/create"
					verb="Create account"
					busyVerb="Creating..."
					subject="the account list"
					conflictIsStale={false}
					{blockers}
					onsubmitted={forgetSecrets}
					{onsuccess}
				>
					{#snippet children(errors, attempted)}
						<fieldset class="fk-group">
							<legend>Account</legend>
							<div class="fk-grid">
								<div class="fk-field">
									<label for="{uid}-id">Account id <span class="fk-req">(required)</span></label>
									<input
										id="{uid}-id"
										name="account_id"
										bind:value={values.account_id}
										maxlength="64"
										autocomplete="off"
										spellcheck="false"
										placeholder="ct-brandA-in"
										aria-invalid={err(errors, attempted, '/account_id') ? 'true' : undefined}
										aria-describedby="{uid}-id-hint {uid}-id-error"
									/>
									<p id="{uid}-id-hint" class="fk-hint">
										Permanent. Integrations name it in their targets; callers never see it.
									</p>
									<FieldError id="{uid}-id-error" message={err(errors, attempted, '/account_id')} />
								</div>
								<div class="fk-field">
									<label for="{uid}-vendor">Vendor <span class="fk-req">(required)</span></label>
									<select
										id="{uid}-vendor"
										name="vendor_id"
										bind:value={values.vendor_id}
										onchange={() => (values.region = '')}
										aria-invalid={err(errors, attempted, '/vendor_id') ? 'true' : undefined}
										aria-describedby="{uid}-vendor-error"
									>
										<option value="">Pick a vendor</option>
										{#each page.items as v (v.vendor_id)}
											<option value={v.vendor_id}>{v.display_name} ({v.vendor_id})</option>
										{/each}
									</select>
									<FieldError
										id="{uid}-vendor-error"
										message={err(errors, attempted, '/vendor_id')}
									/>
								</div>
								<div class="fk-field">
									<label for="{uid}-region">Region <span class="fk-req">(required)</span></label>
									<select
										id="{uid}-region"
										name="region"
										bind:value={values.region}
										disabled={!vendor}
										aria-invalid={err(errors, attempted, '/region') ? 'true' : undefined}
										aria-describedby="{uid}-region-hint {uid}-region-error"
									>
										<option value="">{vendor ? 'Pick a region' : 'Pick the vendor first'}</option>
										{#each vendor?.regions ?? [] as r (r)}<option value={r}>{r}</option>{/each}
									</select>
									<p id="{uid}-region-hint" class="fk-hint">
										One of the vendor's regions; it decides the base URL. It cannot change later.
									</p>
									<FieldError id="{uid}-region-error" message={err(errors, attempted, '/region')} />
								</div>
							</div>
						</fieldset>

						<fieldset class="fk-group">
							<legend>Config and quota</legend>
							<div class="fk-grid">
								<div class="fk-field wide">
									<label for="{uid}-config">Config (JSON object, not secret)</label>
									<textarea
										id="{uid}-config"
										name="config"
										class="fk-mono"
										rows="5"
										bind:value={values.config}
										spellcheck="false"
										aria-invalid={err(errors, attempted, '/config') ? 'true' : undefined}
										aria-describedby="{uid}-config-hint {uid}-config-error"></textarea>
									<p id="{uid}-config-hint" class="fk-hint">
										Transformers read it as <code>account.config.*</code>, e.g.
										<code>{'{"project_id": "4RW-XXX-XXXX"}'}</code>. Never put secrets here: it is
										shown to every viewer.
									</p>
									<FieldError id="{uid}-config-error" message={err(errors, attempted, '/config')} />
								</div>
								<div class="fk-field">
									<label for="{uid}-rps">Quota (requests/s)</label>
									<input
										id="{uid}-rps"
										name="rps"
										inputmode="decimal"
										bind:value={values.rps}
										aria-invalid={err(errors, attempted, '/quota/rps') ? 'true' : undefined}
										aria-describedby="{uid}-rps-error"
									/>
									<FieldError id="{uid}-rps-error" message={err(errors, attempted, '/quota/rps')} />
								</div>
								<div class="fk-field">
									<label for="{uid}-daily">Quota (requests/day)</label>
									<input
										id="{uid}-daily"
										name="daily"
										inputmode="numeric"
										bind:value={values.daily}
										aria-invalid={err(errors, attempted, '/quota/daily') ? 'true' : undefined}
										aria-describedby="{uid}-daily-error"
									/>
									<FieldError
										id="{uid}-daily-error"
										message={err(errors, attempted, '/quota/daily')}
									/>
								</div>
							</div>
						</fieldset>

						<CredentialInputs
							bind:rows={values.credentials}
							error={errors['/credentials'] ?? null}
							showProblems={attempted}
						/>
						{#if cleared}
							<p class="fk-note" role="status">
								The secret values were cleared when you submitted. Type them again to retry.
							</p>
						{/if}
						<div class="fk-group">
							<div class="fk-field">
								<label for="{uid}-exp">Credential expires (UTC)</label>
								<input
									id="{uid}-exp"
									name="credential_expires_at"
									type="datetime-local"
									bind:value={values.credential_expires_at}
									aria-invalid={err(errors, attempted, '/credential_expires_at')
										? 'true'
										: undefined}
									aria-describedby="{uid}-exp-hint {uid}-exp-error"
								/>
								<p id="{uid}-exp-hint" class="fk-hint">
									Optional. When the vendor will stop accepting these credentials, so the rotation
									can happen first.
								</p>
								<FieldError
									id="{uid}-exp-error"
									message={err(errors, attempted, '/credential_expires_at')}
								/>
							</div>
						</div>
					{/snippet}
				</AuthoringForm>
			{/if}
		{/snippet}
	</AsyncView>
	{#snippet denied(role)}
		<p class="fk-note" role="note">
			Read only. Creating accounts needs the {role} role.
			<a href={resolve('/accounts')}>Back to accounts</a>
		</p>
	{/snippet}
</RoleGate>

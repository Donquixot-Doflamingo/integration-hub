<script lang="ts">
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { IntegrationChange } from '$lib/api/types';
	import AsyncView from '$lib/components/AsyncView.svelte';
	import AuthoringForm from '$lib/components/AuthoringForm.svelte';
	import FieldError from '$lib/components/FieldError.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import RoleGate from '$lib/components/RoleGate.svelte';
	import { setFlash } from '$lib/stores/flash.svelte';
	import { blockersFrom } from '$lib/utils/form';
	import type { PageData } from './$types';
	import { DESCRIPTION_MAX, emptyValues, problems } from './integration-form';

	/**
	 * Create an integration (HUB-292, `createIntegration`, editor): id, owner tenant, capability,
	 * grants and the money flag. It has no version yet: the next page is its first draft.
	 */
	let { data }: { data: PageData } = $props();
	const uid = $props.id();

	let values = $state(emptyValues());
	const found = $derived(problems(values));
	const blockers = $derived(blockersFrom(found));
	const err = (errors: Record<string, string | undefined>, attempted: boolean, ptr: string) =>
		errors[ptr] ?? (attempted ? (found[ptr] ?? null) : null);

	async function onsuccess(d: ActionSuccessData) {
		const i = (d.result as IntegrationChange).integration;
		setFlash({
			message: `Created integration ${i.integration_id} (${i.capability}, owned by ${i.owner_tenant}). It has no version yet: add its targets below, save the draft, then publish.`,
			changeId: d.changeId,
			requestId: d.requestId
		});
		await goto(
			resolve('/(app)/integrations/[integration_id]/publish', {
				integration_id: i.integration_id
			})
		);
	}
</script>

<PageHeader
	title="Create integration"
	summary="Callers invoke an integration by its id with a canonical body; which vendor and account serve it stays behind the id."
/>

<RoleGate access={data.access} op="createIntegration">
	<AsyncView result={data.lookups} noun="tenants and capabilities" onretry={invalidateAll}>
		{#snippet children(l)}
			<AuthoringForm
				action="?/create"
				verb="Create integration"
				busyVerb="Creating..."
				subject="the integration list"
				conflictIsStale={false}
				{blockers}
				{onsuccess}
			>
				{#snippet children(errors, attempted)}
					<fieldset class="fk-group">
						<legend>Integration</legend>
						<div class="fk-grid">
							<div class="fk-field">
								<label for="{uid}-id">Integration id <span class="fk-req">(required)</span></label>
								<input
									id="{uid}-id"
									name="integration_id"
									bind:value={values.integration_id}
									maxlength="64"
									autocomplete="off"
									spellcheck="false"
									placeholder="int_evt_brandC"
									aria-invalid={err(errors, attempted, '/integration_id') ? 'true' : undefined}
									aria-describedby="{uid}-id-hint {uid}-id-error"
								/>
								<p id="{uid}-id-hint" class="fk-hint">
									Permanent: callers put it in the URL. A breaking schema change means a new id.
								</p>
								<FieldError
									id="{uid}-id-error"
									message={err(errors, attempted, '/integration_id')}
								/>
							</div>
							<div class="fk-field">
								<label for="{uid}-owner">Owner tenant <span class="fk-req">(required)</span></label>
								<select
									id="{uid}-owner"
									name="owner_tenant"
									bind:value={values.owner_tenant}
									aria-invalid={err(errors, attempted, '/owner_tenant') ? 'true' : undefined}
									aria-describedby="{uid}-owner-error"
								>
									<option value="">Pick a tenant</option>
									{#each l.tenants as t (t.tenant_id)}
										<option value={t.tenant_id}>{t.display_name} ({t.tenant_id})</option>
									{/each}
								</select>
								<FieldError
									id="{uid}-owner-error"
									message={err(errors, attempted, '/owner_tenant')}
								/>
							</div>
							<div class="fk-field">
								<label for="{uid}-cap">Capability <span class="fk-req">(required)</span></label>
								<select
									id="{uid}-cap"
									name="capability"
									bind:value={values.capability}
									aria-invalid={err(errors, attempted, '/capability') ? 'true' : undefined}
									aria-describedby="{uid}-cap-hint {uid}-cap-error"
								>
									<option value="">Pick a capability</option>
									{#each l.capabilities as c (c.capability)}
										<option value={c.capability}
											>{c.capability} ({c.operations.join(', ')}{c.interactive
												? ', interactive'
												: ''})</option
										>
									{/each}
								</select>
								<p id="{uid}-cap-hint" class="fk-hint">
									The canonical request and response callers use. It cannot change later.
								</p>
								<FieldError id="{uid}-cap-error" message={err(errors, attempted, '/capability')} />
							</div>
							<div class="fk-field wide">
								<label for="{uid}-desc">Description</label>
								<textarea
									id="{uid}-desc"
									name="description"
									rows="2"
									maxlength={DESCRIPTION_MAX}
									bind:value={values.description}
									aria-invalid={err(errors, attempted, '/description') ? 'true' : undefined}
									aria-describedby="{uid}-desc-error"></textarea>
								<FieldError
									id="{uid}-desc-error"
									message={err(errors, attempted, '/description')}
								/>
							</div>
							<div class="fk-field wide">
								<label for="{uid}-grants">Other tenants that may call it</label>
								<input
									id="{uid}-grants"
									name="grants"
									list="{uid}-tenants"
									bind:value={values.grants}
									autocomplete="off"
									spellcheck="false"
									placeholder="billing-service, risk-service"
									aria-invalid={err(errors, attempted, '/grants') ? 'true' : undefined}
									aria-describedby="{uid}-grants-hint {uid}-grants-error"
								/>
								<datalist id="{uid}-tenants">
									{#each l.tenants as t (t.tenant_id)}<option value={t.tenant_id}></option>{/each}
								</datalist>
								<p id="{uid}-grants-hint" class="fk-hint">
									Tenant ids, separated by commas. Optional.
								</p>
								<FieldError id="{uid}-grants-error" message={err(errors, attempted, '/grants')} />
							</div>
							<div class="fk-field wide">
								<label class="fk-check">
									<input
										type="checkbox"
										name="moves_money_or_quota"
										bind:checked={values.moves_money_or_quota}
									/>
									Moves money or uses up a quota (billed OTPs, payments): callers must send an Idempotency-Key
									and MCP tools ask for confirmation
								</label>
							</div>
						</div>
					</fieldset>
				{/snippet}
			</AuthoringForm>
		{/snippet}
	</AsyncView>
	{#snippet denied(role)}
		<p class="fk-note" role="note">
			Read only. Creating integrations needs the {role} role.
			<a href={resolve('/integrations')}>Back to integrations</a>
		</p>
	{/snippet}
</RoleGate>

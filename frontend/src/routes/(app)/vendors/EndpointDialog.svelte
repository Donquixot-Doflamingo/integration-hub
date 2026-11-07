<script lang="ts">
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { VendorEndpoint } from '$lib/api/types';
	import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
	import FieldError from '$lib/components/FieldError.svelte';
	import {
		emptyEndpoint,
		endpointProblems,
		METHODS,
		valuesFromEndpoint,
		type FormValues
	} from './endpoint-form';

	/**
	 * Add an endpoint, or the next version of one (HUB-290, `createVendorEndpoint`, admin). Endpoint
	 * versions are immutable, so there is no edit: starting from `from` (the latest version) the
	 * dialog creates `<name>@<n+1>` with the changed values; the name stays fixed.
	 */
	let {
		open = $bindable(false),
		vendorId,
		from = null,
		onsuccess
	}: {
		open?: boolean;
		vendorId: string;
		/** The latest version of the endpoint to create a new version of; null = a new endpoint. */
		from?: VendorEndpoint | null;
		onsuccess: (data: ActionSuccessData) => void;
	} = $props();

	const id = $props.id();
	let values = $state<FormValues>(emptyEndpoint());
	let touched = $state<Record<string, boolean>>({});
	// A fresh form, from the latest version when there is one, every time the dialog opens.
	$effect.pre(() => {
		if (open) {
			values = from ? valuesFromEndpoint(from) : emptyEndpoint();
			touched = {};
		}
	});
	const found = $derived(endpointProblems(values));
	const next = $derived(from ? `${from.name}@${from.version + 1}` : null);
	const title = $derived(from ? `Create ${next} on ${vendorId}` : `Add an endpoint to ${vendorId}`);
</script>

<ConfirmDialog
	bind:open
	{title}
	verb={from ? `Create ${next}` : 'Add endpoint'}
	busyVerb="Creating..."
	action="?/createEndpoint"
	tone="primary"
	subject={vendorId}
	conflictIsStale={false}
	ready={Object.keys(found).length === 0}
	{onsuccess}
>
	{#if from}
		<p>
			Endpoint versions never change. This creates <code>{next}</code> with the values below;
			<code>{from.endpoint_ref}</code> stays as it is, and transformers keep pointing at it until a
			new transformer version targets <code>{next}</code>.
		</p>
	{:else}
		<p>
			Creates <code>&lt;name&gt;@1</code>. Transformers target an endpoint version, so later changes
			become new versions.
		</p>
	{/if}
	{#snippet fields(errors)}
		{@const err = (p: string) => errors[p] ?? (touched[p] ? (found[p] ?? null) : null)}
		{@const describe = (p: string) => (err(p) ? `${id}${p.replace('/', '-')}-error` : undefined)}
		<div class="fk-grid">
			<div class="fk-field">
				<label for="{id}-name">Name <span class="fk-req">(required)</span></label>
				<input
					id="{id}-name"
					name="name"
					bind:value={values.name}
					readonly={!!from}
					onblur={() => (touched['/name'] = true)}
					autocomplete="off"
					spellcheck="false"
					placeholder="upload"
					aria-invalid={err('/name') ? 'true' : undefined}
					aria-describedby={describe('/name')}
				/>
				<FieldError id="{id}-name-error" message={err('/name')} />
			</div>
			<div class="fk-field">
				<label for="{id}-method">Method</label>
				<select id="{id}-method" name="method" bind:value={values.method}>
					{#each METHODS as m (m)}<option value={m}>{m}</option>{/each}
				</select>
			</div>
			<div class="fk-field wide">
				<label for="{id}-path">Path <span class="fk-req">(required)</span></label>
				<input
					id="{id}-path"
					name="path"
					bind:value={values.path}
					onblur={() => (touched['/path'] = true)}
					autocomplete="off"
					spellcheck="false"
					placeholder="/1/upload"
					aria-invalid={err('/path') ? 'true' : undefined}
					aria-describedby={describe('/path')}
				/>
				<FieldError id="{id}-path-error" message={err('/path')} />
			</div>
			<div class="fk-field">
				<label for="{id}-timeout_ms">Timeout (ms)</label>
				<input
					id="{id}-timeout_ms"
					name="timeout_ms"
					inputmode="numeric"
					bind:value={values.timeout_ms}
					onblur={() => (touched['/timeout_ms'] = true)}
					aria-invalid={err('/timeout_ms') ? 'true' : undefined}
					aria-describedby={describe('/timeout_ms')}
				/>
				<FieldError id="{id}-timeout_ms-error" message={err('/timeout_ms')} />
			</div>
			<div class="fk-field">
				<label for="{id}-connect_timeout_ms">Connect timeout (ms)</label>
				<input
					id="{id}-connect_timeout_ms"
					name="connect_timeout_ms"
					inputmode="numeric"
					bind:value={values.connect_timeout_ms}
					onblur={() => (touched['/connect_timeout_ms'] = true)}
					aria-invalid={err('/connect_timeout_ms') ? 'true' : undefined}
					aria-describedby={describe('/connect_timeout_ms')}
				/>
				<FieldError id="{id}-connect_timeout_ms-error" message={err('/connect_timeout_ms')} />
			</div>
			<div class="fk-field">
				<label for="{id}-batch_max">Batch size limit</label>
				<input
					id="{id}-batch_max"
					name="batch_max"
					inputmode="numeric"
					bind:value={values.batch_max}
					onblur={() => (touched['/batch_max'] = true)}
					placeholder="no batching"
					aria-invalid={err('/batch_max') ? 'true' : undefined}
					aria-describedby={describe('/batch_max')}
				/>
				<FieldError id="{id}-batch_max-error" message={err('/batch_max')} />
			</div>
			<div class="fk-field wide">
				<label class="fk-check">
					<input type="checkbox" name="idempotent" bind:checked={values.idempotent} />
					Idempotent: safe to retry and to fail over (the vendor deduplicates or the call has no side
					effects)
				</label>
				<label class="fk-check">
					<input type="checkbox" name="stream" bind:checked={values.stream} />
					Streams its response
				</label>
				<FieldError
					id="{id}-idempotent-error"
					message={errors['/idempotent'] ?? errors['/stream']}
				/>
			</div>
		</div>
	{/snippet}
</ConfirmDialog>

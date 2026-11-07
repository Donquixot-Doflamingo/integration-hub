<script lang="ts">
	import { untrack } from 'svelte';
	import type { ActionSuccessData } from '$lib/api/actions';
	import type { VendorEndpoint } from '$lib/api/types';
	import AuthoringForm from '$lib/components/AuthoringForm.svelte';
	import FieldError from '$lib/components/FieldError.svelte';
	import SchemaEditor from '$lib/components/schema/SchemaEditor.svelte';
	import { blockersFrom } from '$lib/utils/form';
	import {
		emptyEndpoint,
		endpointProblems,
		METHODS,
		pathParams,
		pathSchemaFor,
		valuesFromEndpoint,
		type FormValues,
		type RequestPart
	} from './endpoint-form';

	/**
	 * Add an endpoint, or the next version of one (HUB-290, HUB-436, `createVendorEndpoint`,
	 * admin). Besides method, path and timeouts, an endpoint version declares the request it
	 * accepts, part by part (path parameters, query parameters, non-credential headers, body),
	 * each built with the schema editor; an empty part is not sent. Request transformer samples
	 * are checked against it (check `endpoint_schema`), live calls never. The response schema is
	 * optional and informational. Starting from `from` (the latest version) the form creates
	 * `<name>@<n+1>`; the name stays fixed.
	 */
	let {
		vendorId,
		from = null,
		onsuccess
	}: {
		vendorId: string;
		/** The latest version of the endpoint to create a new version of; null = a new endpoint. */
		from?: VendorEndpoint | null;
		onsuccess: (data: ActionSuccessData) => void | Promise<void>;
	} = $props();

	const uid = $props.id();
	let values = $state<FormValues>(
		untrack(() => (from ? valuesFromEndpoint(from) : emptyEndpoint()))
	);
	const found = $derived(endpointProblems(values));
	const blockers = $derived(blockersFrom(found));
	const next = $derived(from ? `${from.name}@${from.version + 1}` : null);
	const placeholders = $derived(pathParams(values.path));
	const err = (errors: Record<string, string | undefined>, attempted: boolean, ptr: string) =>
		errors[ptr] ?? (attempted ? (found[ptr] ?? null) : null);
	const describe = (ptr: string, extra = '') =>
		`${extra ? `${extra} ` : ''}${uid}${ptr.replace(/\//g, '-')}-error`;

	function setPart(part: RequestPart, s: Record<string, unknown> | null) {
		if (s) values.request_schema[part] = s;
		else delete values.request_schema[part];
	}
	function fillPathParams() {
		const s = pathSchemaFor(values.path.trim());
		setPart('path', s);
	}

	const PARTS: { part: RequestPart; label: string; noun: string; hint: string }[] = [
		{
			part: 'path',
			label: 'Path parameters',
			noun: 'path parameter',
			hint: 'The {name} placeholders of the path. Each one must be declared here, usually required.'
		},
		{
			part: 'query',
			label: 'Query parameters',
			noun: 'query parameter',
			hint: 'e.g. name for GET /?name=Michael. Do not declare query values set from creds.*: credentials are not part of the checked request.'
		},
		{
			part: 'headers',
			label: 'Headers',
			noun: 'header',
			hint: 'Non-credential headers only (Content-Type, X-Api-Version). Headers set from creds.* are never declared.'
		},
		{
			part: 'body',
			label: 'Body',
			noun: 'field',
			hint: 'The JSON body. Leave it empty for endpoints without a body (GET, DELETE).'
		}
	];
</script>

<AuthoringForm
	action="?/createEndpoint"
	verb={next ? `Create ${next}` : 'Add endpoint'}
	busyVerb="Creating..."
	subject={vendorId}
	conflictIsStale={false}
	{blockers}
	{onsuccess}
>
	{#snippet children(errors, attempted)}
		<input type="hidden" name="request_schema" value={JSON.stringify(values.request_schema)} />
		<input
			type="hidden"
			name="response_schema"
			value={values.response_schema ? JSON.stringify(values.response_schema) : ''}
		/>
		<fieldset class="fk-group">
			<legend>Call</legend>
			{#if from}
				<p class="fk-note">
					Endpoint versions never change. This creates <code>{next}</code>;
					<code>{from.endpoint_ref}</code> stays as it is, and transformers keep pointing at it
					until a new transformer version targets <code>{next}</code>.
				</p>
			{/if}
			<div class="fk-grid">
				<div class="fk-field">
					<label for="{uid}-name">Name <span class="fk-req">(required)</span></label>
					<input
						id="{uid}-name"
						name="name"
						bind:value={values.name}
						readonly={!!from}
						autocomplete="off"
						spellcheck="false"
						placeholder="estimate"
						aria-invalid={err(errors, attempted, '/name') ? 'true' : undefined}
						aria-describedby={describe('/name')}
					/>
					<FieldError id={describe('/name')} message={err(errors, attempted, '/name')} />
				</div>
				<div class="fk-field">
					<label for="{uid}-method">Method</label>
					<select id="{uid}-method" name="method" bind:value={values.method}>
						{#each METHODS as m (m)}<option value={m}>{m}</option>{/each}
					</select>
				</div>
				<div class="fk-field wide">
					<label for="{uid}-path">Path <span class="fk-req">(required)</span></label>
					<input
						id="{uid}-path"
						name="path"
						bind:value={values.path}
						autocomplete="off"
						spellcheck="false"
						placeholder="/ or /1/upload or /users/{'{id}'}"
						aria-invalid={err(errors, attempted, '/path') ? 'true' : undefined}
						aria-describedby="{uid}-path-hint {describe('/path')}"
					/>
					<p id="{uid}-path-hint" class="fk-hint">
						On the vendor's region base URL. Path parameters go in braces: <code
							>/users/{'{id}'}</code
						>.
					</p>
					<FieldError id={describe('/path')} message={err(errors, attempted, '/path')} />
				</div>
				{#each [['timeout_ms', 'Timeout (ms)'], ['connect_timeout_ms', 'Connect timeout (ms)']] as const as [k, label] (k)}
					<div class="fk-field">
						<label for="{uid}-{k}">{label}</label>
						<input
							id="{uid}-{k}"
							name={k}
							inputmode="numeric"
							bind:value={values[k]}
							aria-invalid={err(errors, attempted, `/${k}`) ? 'true' : undefined}
							aria-describedby={describe(`/${k}`)}
						/>
						<FieldError id={describe(`/${k}`)} message={err(errors, attempted, `/${k}`)} />
					</div>
				{/each}
				<div class="fk-field">
					<label for="{uid}-batch_max">Batch size limit</label>
					<input
						id="{uid}-batch_max"
						name="batch_max"
						inputmode="numeric"
						bind:value={values.batch_max}
						placeholder="no batching"
						aria-invalid={err(errors, attempted, '/batch_max') ? 'true' : undefined}
						aria-describedby={describe('/batch_max')}
					/>
					<FieldError id={describe('/batch_max')} message={err(errors, attempted, '/batch_max')} />
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
				</div>
			</div>
		</fieldset>

		<fieldset class="fk-group" aria-describedby="{uid}-req-hint">
			<legend>Request it accepts</legend>
			<p id="{uid}-req-hint" class="fk-hint">
				Request transformer samples must produce a request that fits this (a required field missing
				or a wrong type refuses the transformer, check <code>endpoint_schema</code>). Live calls are
				never checked against it. Empty parts are not sent.
			</p>
			<FieldError
				id={describe('/request_schema')}
				message={err(errors, attempted, '/request_schema')}
			/>
			{#each PARTS as p (p.part)}
				{#if p.part === 'path' && placeholders.length}
					<div class="fill">
						<button type="button" class="btn btn-quiet" onclick={fillPathParams}
							>Declare {placeholders.map((n) => `{${n}}`).join(', ')} from the path</button
						>
					</div>
				{/if}
				<SchemaEditor
					label={p.label}
					noun={p.noun}
					hint={p.hint}
					optional
					value={values.request_schema[p.part]}
					error={err(errors, attempted, `/request_schema/${p.part}`)}
					onchange={(s) => setPart(p.part, s)}
				/>
			{/each}
		</fieldset>

		<fieldset class="fk-group">
			<legend>Response <span class="fk-req">(optional, informational)</span></legend>
			<SchemaEditor
				label="Response body"
				hint="What the vendor answers, for reference when writing a response transformer. Never enforced."
				optional
				value={values.response_schema}
				error={err(errors, attempted, '/response_schema')}
				onchange={(s) => (values.response_schema = s)}
			/>
		</fieldset>
	{/snippet}
</AuthoringForm>

<style>
	.fill {
		margin-bottom: calc(-1 * var(--space-2));
	}
</style>

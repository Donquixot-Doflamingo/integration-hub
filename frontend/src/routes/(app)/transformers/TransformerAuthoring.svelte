<script lang="ts">
	import { untrack } from 'svelte';
	import type { SubmitFunction } from '@sveltejs/kit';
	import { enhance } from '$app/forms';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import {
		claimingPointer,
		fromPlainError,
		type ActionFailureData,
		type ActionSuccessData,
		type FieldErrors
	} from '$lib/api/actions';
	import { HubApiError } from '$lib/api/errors';
	import type {
		GoldensRunResult,
		TransformerDryRunResult,
		TransformerVersionChange,
		VendorEndpoint
	} from '$lib/api/types';
	import CodeEditor, { type CodeProblem } from '$lib/components/CodeEditor.svelte';
	import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
	import ErrorPanel from '$lib/components/ErrorPanel.svelte';
	import FieldError from '$lib/components/FieldError.svelte';
	import FormProblems from '$lib/components/FormProblems.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import DryRunResult from '$lib/components/transformer/DryRunResult.svelte';
	import GoldensResult from '$lib/components/transformer/GoldensResult.svelte';
	import SchemaFields from '$lib/components/schema/SchemaFields.svelte';
	import { prettyJson } from '$lib/utils/json-input';
	import { setFlash } from '$lib/stores/flash.svelte';
	import { blockersFrom } from '$lib/utils/form';
	import {
		draftKey,
		draftProblems,
		emptyFixture,
		expectedRequestSkeleton,
		findingsFromError,
		findingsFromRun,
		fieldLabels,
		fieldsFor,
		KINDS,
		needsEndpoint,
		problems,
		type FixtureRow,
		type FormValues,
		type SampleFinding
	} from './authoring';

	/**
	 * Create a transformer version (HUB-293, HUB-436): YAML source and samples (a request
	 * transformer names its vendor endpoint; a response transformer stands alone), then "Run samples"
	 * (`runTransformerGoldens` on the unsaved draft: per-sample diffs and endpoint-schema
	 * violations, shown on the sample and the field that broke) and an optional dry run
	 * (`dryRunTransformer` on the draft, output masked as the API returns it). "Create" stays
	 * locked until a run of this exact draft passed: the Hub refuses a version whose samples fail
	 * (422, checks `goldens_pass` / `endpoint_schema` naming sample and field), and the console
	 * says so before the operator gets there.
	 */
	let {
		mode,
		initial,
		latestVersion = null,
		endpoints
	}: {
		mode: 'new' | 'version';
		initial: FormValues;
		/** The version the new one follows (`expected_latest_version`); null for a new id. */
		latestVersion?: number | null;
		/** Endpoint versions by `vendor.endpoint@n` (suggestions; their request schemas). */
		endpoints: Record<string, VendorEndpoint>;
	} = $props();
	const endpointRefs = $derived(Object.keys(endpoints));

	const uid = $props.id();
	let values = $state<FormValues>(structuredClone(untrack(() => initial)));
	const found = $derived(problems(values, mode));
	const draftFound = $derived(draftProblems(values));
	const key = $derived(draftKey(values));
	const nextRef = $derived(
		`${values.transformer_id.trim() || 'new'}@${latestVersion ? latestVersion + 1 : 1}`
	);

	let apiFields = $state.raw<FieldErrors>({});
	let touched = $state(false);
	const err = (ptr: string) => apiFields[ptr] ?? (touched ? (found[ptr] ?? null) : null);

	// ---- goldens ------------------------------------------------------------------------------
	let goldens = $state.raw<{ key: string; result: GoldensRunResult } | null>(null);
	let goldensError = $state.raw<HubApiError | null>(null);
	let runningGoldens = $state(false);
	const passedNow = $derived(!!goldens && goldens.key === key && goldens.result.passed);
	const stale = $derived(!!goldens && goldens.key !== key);
	// Why samples failed, by sample name, for this exact draft: from a refused create (422, the
	// Hub's word) or else from the last run.
	let refused = $state.raw<{ key: string; findings: Record<string, SampleFinding[]> } | null>(null);
	const findings = $derived<Record<string, SampleFinding[]>>(
		refused && refused.key === key
			? refused.findings
			: goldens && goldens.key === key
				? findingsFromRun(goldens.result)
				: {}
	);
	const checkLabel = (c: string) =>
		c === 'endpoint_schema'
			? 'breaks the endpoint schema'
			: c === 'goldens_pass'
				? 'differs from the expected output'
				: c === 'error'
					? 'threw'
					: c;

	// The endpoint a request transformer targets, when the console knows it (its request schema).
	const endpoint = $derived(
		needsEndpoint(values.kind) ? (endpoints[values.vendor_endpoint.trim()] ?? null) : null
	);
	function fillExpected(f: FixtureRow) {
		if (endpoint) f.json.expected_request = prettyJson(expectedRequestSkeleton(endpoint));
	}

	function failureOf(r: { type: string; status?: number; data?: unknown }, what: string) {
		const d = r.data as ActionFailureData | undefined;
		if (r.type === 'failure') {
			apiFields = { ...(d?.fields ?? {}), ...detailFields(d) };
			return d?.error
				? fromPlainError(d.error)
				: new HubApiError({
						status: r.status ?? 400,
						code: 'VALIDATION_FAILED',
						message: `Fix the marked fields, then ${what} again.`
					});
		}
		return new HubApiError({
			status: r.status ?? 500,
			code: 'UNEXPECTED_RESPONSE',
			message: `The console could not ${what}. Try again.`
		});
	}

	/** API details on the deepest field this page shows (`/fixtures/2/expected_request`, `/yaml`). */
	function detailFields(d: ActionFailureData | undefined): FieldErrors {
		const out: FieldErrors = {};
		const pointers = [
			'/transformer_id',
			'/kind',
			'/vendor_endpoint',
			'/docs_url',
			'/yaml',
			'/fixtures',
			...values.fixtures.flatMap((f, i) => [
				`/fixtures/${i}`,
				`/fixtures/${i}/name`,
				...fieldsFor(values.kind, f.outcome).map((k) => `/fixtures/${i}/${k}`)
			])
		];
		for (const det of d?.error?.details ?? []) {
			const field = claimingPointer(det.pointer, pointers);
			if (field && !out[field]) out[field] = det.message;
		}
		return out;
	}

	const runGoldens: SubmitFunction = ({ cancel }) => {
		touched = true;
		if (runningGoldens || Object.keys(found).length) return cancel();
		runningGoldens = true;
		goldensError = null;
		apiFields = {};
		const sentKey = key;
		return async ({ result: r }) => {
			try {
				if (r.type === 'success')
					goldens = {
						key: sentKey,
						result: (r.data as ActionSuccessData<GoldensRunResult>).result
					};
				else goldensError = failureOf(r, 'run the samples');
			} finally {
				runningGoldens = false;
			}
		};
	};

	// ---- dry run ------------------------------------------------------------------------------
	const responseKind = $derived(values.kind === 'response');
	const sampleOf = (f: FixtureRow | undefined) =>
		f?.json[responseKind ? 'vendor_response' : 'input'] ?? '';
	let dryBody = $state(untrack(() => sampleOf(initial.fixtures[0])));
	let dryHeaders = $state(untrack(() => initial.fixtures[0]?.json.input_headers ?? ''));
	let dryAccount = $state('');
	let dryResult = $state.raw<TransformerDryRunResult | null>(null);
	let dryError = $state.raw<HubApiError | null>(null);
	let dryFields = $state.raw<FieldErrors>({});
	let runningDry = $state(false);
	const compileProblems = $derived<CodeProblem[]>(
		(dryResult?.compile_errors ?? []).map((c) => ({
			line: c.line ?? null,
			message: c.path ? `${c.path}: ${c.message}` : c.message
		}))
	);

	const runDry: SubmitFunction = ({ cancel }) => {
		touched = true;
		if (runningDry || Object.keys(draftFound).length) return cancel();
		runningDry = true;
		dryError = null;
		dryFields = {};
		return async ({ result: r }) => {
			try {
				if (r.type === 'success')
					dryResult = (r.data as ActionSuccessData<TransformerDryRunResult>).result;
				else {
					dryError = failureOf(r, 'run the dry run');
					if (r.type === 'failure')
						dryFields = (r.data as ActionFailureData | undefined)?.fields ?? {};
				}
			} finally {
				runningDry = false;
			}
		};
	};

	// ---- create -------------------------------------------------------------------------------
	let createOpen = $state(false);
	const createBlocked = $derived(
		Object.keys(found).length
			? 'Fix the marked fields first.'
			: !goldens
				? 'Run the samples on this draft first: the Hub refuses a version whose samples fail.'
				: stale
					? 'The draft changed since the last goldens run. Run them again.'
					: !goldens.result.passed
						? `${goldens.result.failed} of ${goldens.result.total} samples fail. Fix the source or the samples; the Hub refuses a version whose samples fail.`
						: null
	);

	async function onCreated(d: ActionSuccessData) {
		const v = (d.result as TransformerVersionChange).transformer_version;
		setFlash({
			message: `Created ${v.ref}: goldens ${v.goldens.total - v.goldens.failed} of ${v.goldens.total} pass. Integrations use it once a draft references ${v.ref} and is published.`,
			changeId: d.changeId,
			requestId: d.requestId
		});
		await goto(
			resolve('/(app)/transformers/[transformer_id]/versions/[version]', {
				transformer_id: v.transformer_id,
				version: String(v.version)
			})
		);
	}
	function onCreateFailed(d: ActionFailureData) {
		apiFields = { ...(d.fields ?? {}), ...detailFields(d) };
		const f = findingsFromError(d.error);
		refused = Object.keys(f).length ? { key, findings: f } : null;
	}

	// ---- fixtures -----------------------------------------------------------------------------
	function addFixture() {
		values.fixtures.push(emptyFixture(values.fixtures.length + 1));
	}
	function removeFixture(i: number) {
		values.fixtures.splice(i, 1);
	}
	const fid = (ptr: string) => `${uid}${ptr.replace(/[^A-Za-z0-9_-]/g, '-')}`;
</script>

{#snippet draftFields()}
	<input type="hidden" name="transformer_id" value={values.transformer_id} />
	<input type="hidden" name="kind" value={values.kind} />
	<input type="hidden" name="vendor_endpoint" value={values.vendor_endpoint} />
	<input type="hidden" name="docs_url" value={values.docs_url} />
	<!-- A textarea, not an input: input values may lose their line breaks. -->
	<textarea name="yaml" hidden readonly tabindex="-1" aria-hidden="true" value={values.yaml}
	></textarea>
	<input type="hidden" name="fixtures" value={JSON.stringify(values.fixtures)} />
{/snippet}

<div class="fk-form authoring">
	<fieldset class="fk-group">
		<legend>Transformer</legend>
		<div class="fk-grid">
			{#if mode === 'new'}
				<div class="fk-field">
					<label for={fid('/transformer_id')}
						>Transformer id <span class="fk-req">(required)</span></label
					>
					<input
						id={fid('/transformer_id')}
						bind:value={values.transformer_id}
						maxlength="64"
						autocomplete="off"
						spellcheck="false"
						placeholder="T3"
						aria-invalid={err('/transformer_id') ? 'true' : undefined}
						aria-describedby="{fid('/transformer_id')}-error"
					/>
					<FieldError id="{fid('/transformer_id')}-error" message={err('/transformer_id')} />
				</div>
			{/if}
			<div class="fk-field">
				<label for={fid('/kind')}>Kind</label>
				<select
					id={fid('/kind')}
					bind:value={values.kind}
					disabled={mode === 'version'}
					aria-invalid={err('/kind') ? 'true' : undefined}
					aria-describedby="{fid('/kind')}-error"
				>
					{#each KINDS as k (k)}<option value={k}>{k}</option>{/each}
				</select>
				<FieldError id="{fid('/kind')}-error" message={err('/kind')} />
			</div>
			{#if needsEndpoint(values.kind)}
				<div class="fk-field">
					<label for={fid('/vendor_endpoint')}>Vendor endpoint</label>
					<input
						id={fid('/vendor_endpoint')}
						list="{uid}-endpoints"
						bind:value={values.vendor_endpoint}
						autocomplete="off"
						spellcheck="false"
						placeholder="clevertap.upload@1"
						aria-invalid={err('/vendor_endpoint') ? 'true' : undefined}
						aria-describedby="{fid('/vendor_endpoint')}-error"
					/>
					<datalist id="{uid}-endpoints">
						{#each endpointRefs as r (r)}<option value={r}></option>{/each}
					</datalist>
					<FieldError id="{fid('/vendor_endpoint')}-error" message={err('/vendor_endpoint')} />
				</div>
			{/if}
			<div class="fk-field wide">
				<label for={fid('/docs_url')}>Vendor documentation the mapping was checked against</label>
				<input
					id={fid('/docs_url')}
					type="url"
					bind:value={values.docs_url}
					autocomplete="off"
					placeholder="https://developer.example.com/docs/upload-events-api"
					aria-invalid={err('/docs_url') ? 'true' : undefined}
					aria-describedby="{fid('/docs_url')}-hint {fid('/docs_url')}-error"
				/>
				<p id="{fid('/docs_url')}-hint" class="fk-hint">Production transformers must cite it.</p>
				<FieldError id="{fid('/docs_url')}-error" message={err('/docs_url')} />
			</div>
		</div>
	</fieldset>

	<div class="fk-group">
		<CodeEditor
			bind:value={values.yaml}
			label="Source"
			labelNote="(transformer YAML)"
			name="yaml-source"
			rows={18}
			hint="The transformer DSL (DESIGN §7). Credentials only through creds.* in headers, query or auth."
			problemsTitle="Fix the source"
			problems={compileProblems}
			error={err('/yaml')}
		/>
	</div>

	{#if endpoint}
		<details class="fk-group accepts">
			<summary>What {values.vendor_endpoint.trim()} accepts</summary>
			<p class="fk-hint">
				Each sample's produced request must fit this, or the Hub refuses the transformer (check
				<code>endpoint_schema</code>).
			</p>
			{#each ['path', 'query', 'headers', 'body'] as const as part (part)}
				{#if endpoint.request_schema?.[part] !== undefined}
					<h3>{part}</h3>
					<SchemaFields
						schema={endpoint.request_schema[part]}
						label="{part} of {values.vendor_endpoint.trim()}"
						empty="Declared, without fields."
					/>
				{/if}
			{/each}
		</details>
	{/if}

	<fieldset class="fk-group" aria-describedby="{fid('/fixtures')}-hint">
		<legend>Samples <span class="fk-req">(at least one)</span></legend>
		<p id="{fid('/fixtures')}-hint" class="fk-hint">
			{responseKind
				? 'Each sample is a vendor response (status, headers, body) and the output the caller must get, or the error it must produce. The output goes back unchanged: no schema check.'
				: 'Each sample is the caller input (body and optional headers) and the exact vendor request it must produce: method, resolved path, and the query, non-credential headers and body that apply.'}
			JSON objects. The Hub runs the transformer on every sample and refuses it when an output differs{responseKind
				? ''
				: " or breaks the endpoint's request schema"}.
		</p>
		<FieldError id="{fid('/fixtures')}-error" message={err('/fixtures')} />
		<ol class="fixtures">
			{#each values.fixtures as f, i (i)}
				{@const at = `/fixtures/${i}`}
				{@const failed = findings[f.name.trim()] ?? []}
				<li class="fixture" class:failed={failed.length > 0} aria-labelledby="{fid(at)}-h">
					<div class="fixture-head">
						<h3 id="{fid(at)}-h">Sample {i + 1}{f.name ? `: ${f.name}` : ''}</h3>
						<button
							type="button"
							class="btn btn-quiet"
							onclick={() => removeFixture(i)}
							aria-label="Remove sample {f.name || i + 1}"
							><Icon name="close" size={14} />Remove</button
						>
					</div>
					{#if failed.length}
						<div class="findings" role="group" aria-label="Why sample {f.name} fails">
							<p><Icon name="alert" size={14} /> This sample fails:</p>
							<ul>
								{#each failed as x, k (k)}
									<li>
										{#if x.field}<code class="field">{x.field}</code>{/if}
										<span class="check">{checkLabel(x.check)}</span>: {x.message}
									</li>
								{/each}
							</ul>
						</div>
					{/if}
					<FieldError id="{fid(at)}-error" message={err(at)} />
					<div class="fk-grid">
						<div class="fk-field">
							<label for={fid(`${at}/name`)}>Name</label>
							<input
								id={fid(`${at}/name`)}
								bind:value={f.name}
								autocomplete="off"
								spellcheck="false"
								aria-invalid={err(`${at}/name`) ? 'true' : undefined}
								aria-describedby="{fid(`${at}/name`)}-error"
							/>
							<FieldError id="{fid(`${at}/name`)}-error" message={err(`${at}/name`)} />
						</div>
						{#if responseKind}
							<div class="fk-field">
								<span class="fk-label" id="{fid(at)}-outcome">Expects</span>
								<div class="outcome" role="radiogroup" aria-labelledby="{fid(at)}-outcome">
									<label class="fk-check"
										><input type="radio" value="response" bind:group={f.outcome} /> an output</label
									>
									<label class="fk-check"
										><input type="radio" value="error" bind:group={f.outcome} /> an error (from its errors
										rules)</label
									>
								</div>
							</div>
						{/if}
						{#each fieldsFor(values.kind, f.outcome) as k (k)}
							{@const p = `${at}/${k}`}
							<div class="fk-field wide">
								<label for={fid(p)}>{fieldLabels[k]}</label>
								<textarea
									id={fid(p)}
									class="fk-mono"
									rows={k === 'account_config' || k === 'input_headers' ? 2 : 6}
									value={f.json[k] ?? ''}
									oninput={(e) => (f.json[k] = e.currentTarget.value)}
									spellcheck="false"
									placeholder={k === 'input_headers'
										? '{ "X-Request-Source": "checkout" }'
										: k === 'expected_request'
											? '{ "method": "GET", "path": "/", "query": { "name": "Michael" } }'
											: undefined}
									aria-invalid={err(p) ? 'true' : undefined}
									aria-describedby="{fid(p)}-error"></textarea>
								{#if k === 'expected_request' && endpoint}
									<div>
										<button type="button" class="btn btn-quiet" onclick={() => fillExpected(f)}
											>Start from what {values.vendor_endpoint.trim()} accepts</button
										>
									</div>
								{/if}
								<FieldError id="{fid(p)}-error" message={err(p)} />
							</div>
						{/each}
					</div>
				</li>
			{/each}
		</ol>
		<div><button type="button" class="btn" onclick={addFixture}>Add sample</button></div>
	</fieldset>

	{#if touched && Object.keys(found).length}
		<FormProblems title="Fix these first" items={blockersFrom(found)} />
	{/if}

	<section class="fk-group" aria-labelledby="{uid}-goldens-h">
		<h2 id="{uid}-goldens-h">Run the samples</h2>
		<form method="POST" action="?/runDraftGoldens" use:enhance={runGoldens}>
			{@render draftFields()}
			<button type="submit" class="btn" aria-disabled={runningGoldens ? 'true' : undefined}>
				<Icon name="retry" size={16} />{runningGoldens
					? 'Running samples...'
					: 'Run samples on this draft'}
			</button>
		</form>
		{#if goldensError}<ErrorPanel
				error={goldensError}
				context="Could not run the samples"
				headingLevel={3}
			/>{/if}
		{#if goldens}
			{#if stale}
				<p class="fk-note" role="status">
					<Icon name="alert" size={14} /> The draft changed since this run: run the samples again.
				</p>
			{/if}
			<GoldensResult result={goldens.result} refLabel="This draft" />
		{/if}
	</section>

	<section class="fk-group" aria-labelledby="{uid}-dry-h">
		<h2 id="{uid}-dry-h">Dry run</h2>
		<form method="POST" action="?/dryRunDraft" use:enhance={runDry} class="dry" novalidate>
			{@render draftFields()}
			<input type="hidden" name="mode" value={responseKind ? 'response' : 'request'} />
			<div class="fk-field">
				<label for="{uid}-dry-body"
					>{responseKind
						? 'Vendor response (JSON: status, headers, body)'
						: 'Caller input body (JSON)'}</label
				>
				<textarea
					id="{uid}-dry-body"
					name={responseKind ? 'vendor_response' : 'input'}
					class="fk-mono"
					rows="6"
					bind:value={dryBody}
					spellcheck="false"
					aria-invalid={dryFields.input || dryFields.vendor_response ? 'true' : undefined}
					aria-describedby="{uid}-dry-body-error"></textarea>
				<FieldError
					id="{uid}-dry-body-error"
					message={dryFields.input ?? dryFields.vendor_response}
				/>
			</div>
			{#if !responseKind}
				<div class="fk-field">
					<label for="{uid}-dry-headers">Caller headers (optional JSON object of strings)</label>
					<textarea
						id="{uid}-dry-headers"
						name="input_headers"
						class="fk-mono"
						rows="2"
						bind:value={dryHeaders}
						spellcheck="false"
						placeholder={'{ "X-Request-Source": "checkout" }'}
						aria-invalid={dryFields.input_headers ? 'true' : undefined}
						aria-describedby="{uid}-dry-headers-error"></textarea>
					<FieldError id="{uid}-dry-headers-error" message={dryFields.input_headers} />
				</div>
			{/if}
			<div class="fk-field">
				<label for="{uid}-dry-account">Account (optional: its config, credentials redacted)</label>
				<input
					id="{uid}-dry-account"
					name="account_id"
					bind:value={dryAccount}
					autocomplete="off"
					spellcheck="false"
					placeholder="ct-brandA-in"
					aria-invalid={dryFields.account_id ? 'true' : undefined}
					aria-describedby="{uid}-dry-account-error"
				/>
				<FieldError id="{uid}-dry-account-error" message={dryFields.account_id} />
			</div>
			<div>
				<button type="submit" class="btn" aria-disabled={runningDry ? 'true' : undefined}>
					{runningDry ? 'Running...' : 'Dry run this draft'}
				</button>
			</div>
		</form>
		{#if dryError}<ErrorPanel
				error={dryError}
				context="Could not run the dry run"
				headingLevel={3}
			/>{/if}
		{#if dryResult}<DryRunResult result={dryResult} refLabel="this draft" />{/if}
	</section>

	<section class="fk-group" aria-labelledby="{uid}-create-h">
		<h2 id="{uid}-create-h">Create {nextRef}</h2>
		<p class="fk-note">
			A version never changes once created. Integrations keep using their current version until a
			draft references the new one and is published.
		</p>
		<div class="fk-actions">
			<button
				type="button"
				class="btn btn-primary"
				disabled={!!createBlocked}
				aria-describedby={createBlocked ? `${uid}-blocked` : undefined}
				onclick={() => (createOpen = true)}>Create {nextRef}</button
			>
		</div>
		{#if createBlocked}<p id="{uid}-blocked" class="fk-note">{createBlocked}</p>{/if}
		{#if passedNow}
			<p class="fk-note" role="status">
				<Icon name="check" size={14} /> All samples pass on this draft.
			</p>
		{/if}
	</section>
</div>

<ConfirmDialog
	bind:open={createOpen}
	title="Create {nextRef}"
	verb="Create {nextRef}"
	busyVerb="Creating..."
	action="?/createVersion"
	tone="primary"
	subject={values.transformer_id || 'the transformer'}
	conflictIsStale={mode === 'version'}
	onsuccess={onCreated}
	onfailure={onCreateFailed}
>
	<p>
		The Hub compiles the source and runs all {values.fixtures.length}
		{values.fixtures.length === 1 ? 'sample' : 'samples'} again{needsEndpoint(values.kind)
			? `, checking each produced request against ${values.vendor_endpoint || 'the endpoint'}'s request schema`
			: ''}; it refuses the version if any fails. The new version is immutable.
	</p>
	{#snippet fields()}
		{@render draftFields()}
		{#if latestVersion}<input
				type="hidden"
				name="expected_latest_version"
				value={latestVersion}
			/>{/if}
	{/snippet}
</ConfirmDialog>

<style>
	.authoring {
		max-width: 72rem;
	}
	.authoring h2 {
		margin: 0;
		font-size: var(--text-lg);
	}
	.fixtures {
		display: grid;
		gap: var(--space-3);
		margin: 0;
		padding: 0;
		list-style: none;
	}
	.fixture {
		display: grid;
		gap: var(--space-2);
		padding: var(--space-3);
		border: 1px solid var(--rule);
		border-left: 3px solid var(--signal);
		border-radius: var(--radius-sm);
		background: var(--paper);
		min-width: 0;
	}
	.fixture-head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
	}
	.fixture-head h3 {
		margin: 0;
		font-size: var(--text-md);
		overflow-wrap: anywhere;
	}
	.outcome {
		display: grid;
		gap: var(--space-1);
	}
	.dry {
		display: grid;
		gap: var(--space-3);
	}
	.fixture.failed {
		border-left-color: var(--status-danger);
		border-left-width: 6px;
	}
	.findings {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--status-danger);
		border-radius: var(--radius-sm);
		background: var(--status-danger-wash);
		font-size: var(--text-sm);
	}
	.findings p {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		margin: 0 0 var(--space-1);
		color: var(--status-danger);
		font-weight: 700;
	}
	.findings ul {
		display: grid;
		gap: 0.15rem;
		margin: 0;
		padding-left: var(--space-4);
	}
	.findings li {
		overflow-wrap: anywhere;
	}
	.findings .field {
		margin-right: 0.35rem;
		font-weight: 700;
	}
	.findings .check {
		font-weight: 600;
	}
	.accepts summary {
		cursor: pointer;
		font-weight: 700;
	}
	.accepts h3 {
		margin: var(--space-2) 0 0;
		font-size: var(--text-sm);
	}
</style>

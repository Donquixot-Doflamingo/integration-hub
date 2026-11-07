<script lang="ts">
	import type {
		AccountSummary,
		DeliveryMode,
		IntegrationSpec,
		TransformerSummary,
		VendorSummary
	} from '$lib/api/types';
	import FieldError from '$lib/components/FieldError.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import { pointerToPath } from '$lib/utils/diff';
	import type { SpecProblem } from './draft';
	import {
		DELIVERY_MODES,
		deliveryLabels,
		emptyTarget,
		escapeToken,
		fieldPointers,
		ON_CIRCUIT_OPEN,
		parseSchedule,
		PAYLOADS,
		PENDING,
		problemsByField,
		setOwnPair,
		transformerOptions,
		unknownOperations,
		weightsTotal,
		yamlOnlySections
	} from './spec-form';

	/**
	 * Structured draft editor (HUB-292). Edits `spec` in place (a `$state` object owned by the
	 * draft editor, which renders it to YAML for saving and for the YAML tab). Targets: vendor,
	 * account, weight, the default transformer pair and, for a multi-operation capability, a pair
	 * per operation (`TargetRef.operations`, e.g. `send_otp` on the default pair and `verify_otp`
	 * on its own). Problems (publish checks, API validation, the console's own checks) show on the
	 * field their JSON pointer names; the rest are listed above with their path.
	 */
	let {
		spec = $bindable(),
		operations,
		vendors = [],
		accounts = [],
		transformers = [],
		problems = [],
		disabled = false
	}: {
		spec: IntegrationSpec;
		/**
		 * The capability's operation names (`default` for single-operation capabilities); empty when
		 * the capability could not be read (the spec's own operations are shown then).
		 */
		operations: string[];
		vendors?: VendorSummary[];
		accounts?: AccountSummary[];
		transformers?: TransformerSummary[];
		problems?: SpecProblem[];
		disabled?: boolean;
	} = $props();

	const uid = $props.id();
	const multi = $derived(operations.length > 1);
	const total = $derived(weightsTotal(spec));
	const pointers = $derived(fieldPointers(spec, operations));
	const located = $derived(problemsByField(problems, pointers));
	const yamlOnly = $derived(yamlOnlySections(spec));

	// Hub messages start with the registry file they were found in; the field already says where.
	const tidy = (m: string) => m.replace(/^[\w./-]+\.ya?ml: /, '');
	const at = (ptr: string) => located.byField.get(ptr)?.map(tidy).join('\n') ?? null;
	const fid = (ptr: string) => `${uid}${ptr.replace(/[^A-Za-z0-9_-]/g, '-')}`;
	const describe = (ptr: string) => (at(ptr) ? `${fid(ptr)}-error` : undefined);
	const invalid = (ptr: string) => (at(ptr) ? 'true' : undefined);

	const opsFor = (t: IntegrationSpec['targets'][number]) =>
		operations.length ? operations : Object.keys(t.operations ?? {});
	const accountsOf = (vendor: string) =>
		accounts.filter((a) => (!vendor || a.vendor_id === vendor) && a.state !== 'retired');

	function addTarget() {
		spec.targets.push(emptyTarget(spec.targets.length ? 0 : 100));
	}
	function removeTarget(i: number) {
		spec.targets.splice(i, 1);
	}
	// The wait window only exists in wait mode: the Hub refuses `wait_ms` with any other mode.
	function setMode(mode: DeliveryMode) {
		spec.delivery.mode = mode;
		if (mode !== 'wait') delete spec.delivery.wait_ms;
	}
	function setNumber(obj: Record<string, unknown>, key: string, raw: string) {
		const v = raw.trim();
		if (!v) delete obj[key];
		else if (/^\d+$/.test(v)) obj[key] = Number(v);
	}
	let scheduleText = $state(spec.delivery.retry?.schedule_ms?.join(', ') ?? '');
	let scheduleError = $state<string | null>(null);
	function setSchedule(raw: string) {
		scheduleText = raw;
		const parsed = parseSchedule(raw);
		if (parsed === null) {
			scheduleError = 'Whole numbers of milliseconds, separated by commas.';
			return;
		}
		scheduleError = null;
		const retry = (spec.delivery.retry ??= {});
		if (parsed.length) retry.schedule_ms = parsed;
		else delete retry.schedule_ms;
		if (!Object.keys(retry).length) delete spec.delivery.retry;
	}
</script>

{#snippet refInput(
	ptr: string,
	label: string,
	options: string[],
	get: () => string,
	set: (v: string) => void
)}
	<div class="fk-field">
		<label for={fid(ptr)}>{label}</label>
		<input
			id={fid(ptr)}
			list="{fid(ptr)}-options"
			value={get()}
			oninput={(e) => set(e.currentTarget.value.trim())}
			autocomplete="off"
			spellcheck="false"
			placeholder="T1@7"
			aria-invalid={invalid(ptr)}
			aria-describedby={describe(ptr)}
		/>
		<datalist id="{fid(ptr)}-options">
			{#each options as o (o)}<option value={o}></option>{/each}
		</datalist>
		<FieldError id="{fid(ptr)}-error" message={at(ptr)} />
	</div>
{/snippet}

<div class="spec-form">
	{#if located.unclaimed.length}
		<div class="fk-summary" role="alert">
			<p>
				<Icon name="alert" size={16} /> Problems outside these fields (fix them in the YAML tab)
			</p>
			<ul>
				{#each located.unclaimed as p, i (i)}
					<li><code>{pointerToPath(p.pointer)}</code>: {p.message}</li>
				{/each}
			</ul>
		</div>
	{/if}

	<fieldset
		class="fk-group"
		{disabled}
		aria-describedby="{fid('/targets')}-total{at('/targets') ? ` ${fid('/targets')}-error` : ''}"
	>
		<legend>Targets</legend>
		<p
			id="{fid('/targets')}-total"
			class="total"
			class:off={spec.targets.length > 0 && total !== 100}
			role="status"
		>
			Weights total {total}{spec.targets.length > 0 && total !== 100
				? ': they must total 100'
				: ''}.
		</p>
		<FieldError id="{fid('/targets')}-error" message={at('/targets')} />
		<ol class="targets">
			{#each spec.targets as t, i (i)}
				{@const p = `/targets/${i}`}
				<li class="target" aria-labelledby="{fid(p)}-h">
					<div class="target-head">
						<h3 id="{fid(p)}-h">Target {i + 1}{t.account_id ? `: ${t.account_id}` : ''}</h3>
						<button
							type="button"
							class="btn btn-quiet"
							onclick={() => removeTarget(i)}
							aria-label="Remove target {i + 1}"><Icon name="close" size={14} />Remove</button
						>
					</div>
					<FieldError id="{fid(p)}-error" message={at(p)} />
					<div class="fk-grid">
						<div class="fk-field">
							<label for={fid(`${p}/vendor_id`)}>Vendor</label>
							<input
								id={fid(`${p}/vendor_id`)}
								list="{uid}-vendors"
								bind:value={t.vendor_id}
								autocomplete="off"
								spellcheck="false"
								aria-invalid={invalid(`${p}/vendor_id`)}
								aria-describedby={describe(`${p}/vendor_id`)}
							/>
							<FieldError id="{fid(`${p}/vendor_id`)}-error" message={at(`${p}/vendor_id`)} />
						</div>
						<div class="fk-field">
							<label for={fid(`${p}/account_id`)}>Account</label>
							<input
								id={fid(`${p}/account_id`)}
								list="{fid(p)}-accounts"
								bind:value={t.account_id}
								autocomplete="off"
								spellcheck="false"
								aria-invalid={invalid(`${p}/account_id`)}
								aria-describedby={describe(`${p}/account_id`)}
							/>
							<datalist id="{fid(p)}-accounts">
								{#each accountsOf(t.vendor_id) as a (a.account_id)}
									<option value={a.account_id}>{a.vendor_id}, {a.region}, {a.state}</option>
								{/each}
							</datalist>
							<FieldError id="{fid(`${p}/account_id`)}-error" message={at(`${p}/account_id`)} />
						</div>
						<div class="fk-field">
							<label for={fid(`${p}/weight`)}>Weight (%)</label>
							<input
								id={fid(`${p}/weight`)}
								type="number"
								min="0"
								max="100"
								step="1"
								bind:value={t.weight}
								aria-invalid={invalid(`${p}/weight`)}
								aria-describedby={describe(`${p}/weight`)}
							/>
							<FieldError id="{fid(`${p}/weight`)}-error" message={at(`${p}/weight`)} />
						</div>
					</div>

					<h4 class="sub">
						{multi ? 'Default transformers (operations without their own)' : 'Transformers'}
					</h4>
					<FieldError id="{fid(`${p}/transformer`)}-error" message={at(`${p}/transformer`)} />
					<div class="fk-grid">
						{@render refInput(
							`${p}/transformer/request`,
							'Request transformer',
							transformerOptions(transformers, 'request', t.vendor_id),
							() => t.transformer.request,
							(v) => (t.transformer.request = v)
						)}
						{@render refInput(
							`${p}/transformer/response`,
							'Response transformer',
							transformerOptions(transformers, 'response', t.vendor_id),
							() => t.transformer.response,
							(v) => (t.transformer.response = v)
						)}
					</div>

					{#if multi || t.operations}
						<h4 class="sub">Per operation</h4>
						<FieldError id="{fid(`${p}/operations`)}-error" message={at(`${p}/operations`)} />
						<ul class="ops">
							{#each opsFor(t) as op (op)}
								{@const o = `${p}/operations/${escapeToken(op)}`}
								{@const own = !!t.operations?.[op]}
								<li class="op" aria-labelledby="{fid(o)}-name">
									<div class="op-head">
										<code id="{fid(o)}-name">{op}</code>
										<label class="fk-check">
											<input
												type="checkbox"
												checked={own}
												onchange={(e) => setOwnPair(t, op, e.currentTarget.checked)}
											/>
											Own transformers for {op}
										</label>
										{#if !own}<span class="fk-hint">Uses the default pair.</span>{/if}
									</div>
									<FieldError id="{fid(o)}-error" message={at(o)} />
									{#if own && t.operations?.[op]}
										{@const pair = t.operations[op]}
										<div class="fk-grid">
											{@render refInput(
												`${o}/request`,
												`Request transformer for ${op}`,
												transformerOptions(transformers, 'request', t.vendor_id),
												() => pair.request,
												(v) => (pair.request = v)
											)}
											{@render refInput(
												`${o}/response`,
												`Response transformer for ${op}`,
												transformerOptions(transformers, 'response', t.vendor_id),
												() => pair.response,
												(v) => (pair.response = v)
											)}
										</div>
									{/if}
								</li>
							{/each}
							{#each operations.length ? unknownOperations(t, operations) : [] as op (op)}
								{@const o = `${p}/operations/${escapeToken(op)}`}
								<li class="op unknown">
									<div class="op-head">
										<code>{op}</code>
										<span class="fk-error"
											><Icon name="alert" size={14} />Not an operation of this capability</span
										>
										<button
											type="button"
											class="btn btn-quiet"
											onclick={() => setOwnPair(t, op, false)}>Remove {op}</button
										>
									</div>
									<FieldError id="{fid(o)}-error" message={at(o)} />
								</li>
							{/each}
						</ul>
					{/if}
				</li>
			{/each}
		</ol>
		<datalist id="{uid}-vendors">
			{#each vendors as v (v.vendor_id)}<option value={v.vendor_id}>{v.display_name}</option>{/each}
		</datalist>
		<div><button type="button" class="btn" onclick={addTarget}>Add target</button></div>
	</fieldset>

	<fieldset class="fk-group" {disabled}>
		<legend>Delivery</legend>
		<FieldError id="{fid('/delivery')}-error" message={at('/delivery')} />
		<div class="fk-grid">
			<div class="fk-field">
				<label for={fid('/delivery/mode')}>Mode</label>
				<select
					id={fid('/delivery/mode')}
					value={spec.delivery.mode}
					onchange={(e) => setMode(e.currentTarget.value as DeliveryMode)}
					aria-invalid={invalid('/delivery/mode')}
					aria-describedby={describe('/delivery/mode')}
				>
					{#each DELIVERY_MODES as m (m)}<option value={m}>{deliveryLabels[m]}</option>{/each}
				</select>
				<FieldError id="{fid('/delivery/mode')}-error" message={at('/delivery/mode')} />
			</div>
			{#if spec.delivery.mode === 'wait'}
				<div class="fk-field">
					<label for={fid('/delivery/wait_ms')}>Wait window (ms)</label>
					<input
						id={fid('/delivery/wait_ms')}
						inputmode="numeric"
						value={spec.delivery.wait_ms ?? ''}
						oninput={(e) => setNumber(spec.delivery, 'wait_ms', e.currentTarget.value)}
						placeholder="3000"
						aria-invalid={invalid('/delivery/wait_ms')}
						aria-describedby={describe('/delivery/wait_ms')}
					/>
					<FieldError id="{fid('/delivery/wait_ms')}-error" message={at('/delivery/wait_ms')} />
				</div>
			{/if}
			<div class="fk-field">
				<label for={fid('/delivery/ttl_ms')}>Request lifetime (ms)</label>
				<input
					id={fid('/delivery/ttl_ms')}
					inputmode="numeric"
					value={spec.delivery.ttl_ms ?? ''}
					oninput={(e) => setNumber(spec.delivery, 'ttl_ms', e.currentTarget.value)}
					placeholder="capability default"
					aria-invalid={invalid('/delivery/ttl_ms')}
					aria-describedby={describe('/delivery/ttl_ms')}
				/>
				<FieldError id="{fid('/delivery/ttl_ms')}-error" message={at('/delivery/ttl_ms')} />
			</div>
			<div class="fk-field">
				<label for={fid('/delivery/on_circuit_open')}>When the breaker is open</label>
				<select
					id={fid('/delivery/on_circuit_open')}
					value={spec.delivery.on_circuit_open ?? 'fail_fast'}
					onchange={(e) =>
						(spec.delivery.on_circuit_open = e.currentTarget
							.value as (typeof ON_CIRCUIT_OPEN)[number])}
					aria-invalid={invalid('/delivery/on_circuit_open')}
					aria-describedby={describe('/delivery/on_circuit_open')}
				>
					<option value="fail_fast">Fail fast (503 with retry hint)</option>
					<option value="failover">Fail over to the failover chain</option>
					<option value="queue_if_allowed">Queue when the mode allows</option>
				</select>
				<FieldError
					id="{fid('/delivery/on_circuit_open')}-error"
					message={at('/delivery/on_circuit_open')}
				/>
			</div>
			<div class="fk-field">
				<label for={fid('/delivery/pending')}>Queued requests at a cutover</label>
				<select
					id={fid('/delivery/pending')}
					value={spec.delivery.pending ?? 're_resolve'}
					onchange={(e) =>
						(spec.delivery.pending = e.currentTarget.value as (typeof PENDING)[number])}
				>
					<option value="re_resolve">Re-resolve to the active version when sent</option>
					<option value="pinned">Stay pinned to the version they arrived on</option>
				</select>
				<FieldError id="{fid('/delivery/pending')}-error" message={at('/delivery/pending')} />
			</div>
			<div class="fk-field">
				<label for={fid('/delivery/retry/max_attempts')}>Maximum attempts</label>
				<input
					id={fid('/delivery/retry/max_attempts')}
					inputmode="numeric"
					value={spec.delivery.retry?.max_attempts ?? ''}
					oninput={(e) => {
						const retry = (spec.delivery.retry ??= {});
						setNumber(retry, 'max_attempts', e.currentTarget.value);
						if (!Object.keys(retry).length) delete spec.delivery.retry;
					}}
					placeholder="Hub default"
					aria-invalid={invalid('/delivery/retry/max_attempts')}
					aria-describedby={describe('/delivery/retry/max_attempts')}
				/>
				<FieldError
					id="{fid('/delivery/retry/max_attempts')}-error"
					message={at('/delivery/retry/max_attempts')}
				/>
			</div>
			<div class="fk-field">
				<label for={fid('/delivery/retry/schedule_ms')}>Retry schedule (ms, comma-separated)</label>
				<input
					id={fid('/delivery/retry/schedule_ms')}
					value={scheduleText}
					oninput={(e) => setSchedule(e.currentTarget.value)}
					placeholder="1000, 5000, 30000"
					aria-invalid={scheduleError || at('/delivery/retry/schedule_ms') ? 'true' : undefined}
					aria-describedby="{fid('/delivery/retry/schedule_ms')}-error"
				/>
				<FieldError
					id="{fid('/delivery/retry/schedule_ms')}-error"
					message={scheduleError ?? at('/delivery/retry/schedule_ms') ?? at('/delivery/retry')}
				/>
			</div>
		</div>
	</fieldset>

	<fieldset class="fk-group" {disabled}>
		<legend>Audit</legend>
		<FieldError id="{fid('/audit')}-error" message={at('/audit')} />
		<div class="fk-grid">
			<div class="fk-field">
				<label for={fid('/audit/payloads')}>Payloads in the audit trail</label>
				<select
					id={fid('/audit/payloads')}
					bind:value={spec.audit.payloads}
					aria-invalid={invalid('/audit/payloads')}
					aria-describedby={describe('/audit/payloads')}
				>
					{#each PAYLOADS as p (p)}
						<option value={p}
							>{{
								full: 'Full',
								masked: 'Masked (default)',
								sealed: 'Sealed (Aadhaar, PAN, bureau: unseal is audited)',
								metadata_only: 'Metadata only'
							}[p]}</option
						>
					{/each}
				</select>
				<FieldError id="{fid('/audit/payloads')}-error" message={at('/audit/payloads')} />
			</div>
			<div class="fk-field">
				<label for={fid('/audit/retention_ms')}>Retention (ms)</label>
				<input
					id={fid('/audit/retention_ms')}
					inputmode="numeric"
					value={spec.audit.retention_ms ?? ''}
					oninput={(e) => setNumber(spec.audit, 'retention_ms', e.currentTarget.value)}
					placeholder="Hub default"
					aria-invalid={invalid('/audit/retention_ms')}
					aria-describedby={describe('/audit/retention_ms')}
				/>
				<FieldError id="{fid('/audit/retention_ms')}-error" message={at('/audit/retention_ms')} />
			</div>
		</div>
	</fieldset>

	{#if yamlOnly.length}
		<p class="fk-note" role="note">
			Kept as they are: {yamlOnly.join(', ')}. Edit them in the YAML tab.
		</p>
	{/if}
</div>

<style>
	.spec-form {
		display: grid;
		gap: var(--space-4);
	}
	.total {
		margin: 0;
		font-size: var(--text-sm);
		font-weight: 700;
	}
	.total.off {
		color: var(--status-danger);
	}
	.targets,
	.ops {
		display: grid;
		gap: var(--space-3);
		margin: 0;
		padding: 0;
		list-style: none;
	}
	.target {
		display: grid;
		gap: var(--space-2);
		padding: var(--space-3);
		border: 1px solid var(--rule);
		border-left: 3px solid var(--signal);
		border-radius: var(--radius-sm);
		background: var(--paper);
		min-width: 0;
	}
	.target-head,
	.op-head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
	}
	.op-head {
		justify-content: flex-start;
	}
	.target-head h3 {
		margin: 0;
		font-size: var(--text-md);
		overflow-wrap: anywhere;
	}
	.sub {
		margin: var(--space-2) 0 0;
		font-size: var(--text-sm);
		font-weight: 700;
	}
	.op {
		display: grid;
		gap: var(--space-2);
		padding: var(--space-2) var(--space-3);
		border: 1px dashed var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
	}
	.op.unknown {
		border-color: var(--status-danger);
	}
</style>

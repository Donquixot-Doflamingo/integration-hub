<script lang="ts">
	import type { IntegrationSpec } from '$lib/api/types';
	import FieldError from '$lib/components/FieldError.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import SchemaEditor from '$lib/components/schema/SchemaEditor.svelte';
	import {
		addOperation,
		DEFAULT_OPERATION,
		escapeToken,
		operationNameProblem,
		removeOperation,
		renameOperation
	} from './spec-form';

	/**
	 * The operations of a draft (HUB-436, ADR-0023): what callers invoke and what they must send.
	 * Each operation has a name (`default` for a single-operation integration), a caller-facing
	 * description, whether it is idempotent, whether live calls are checked against its request
	 * schema (`validate_input`) and the request schema itself: the body (required) and the caller
	 * headers the request transformer may read (optional), each built with the SchemaEditor.
	 * Edits `spec.operations` in place; renaming or removing an operation also moves or drops the
	 * per-operation transformer pairs of the targets.
	 */
	let {
		spec,
		interactive = false,
		at,
		fid,
		disabled = false
	}: {
		spec: IntegrationSpec;
		interactive?: boolean;
		/** The problem shown at a JSON pointer into the spec, or null. */
		at: (ptr: string) => string | null;
		/** The element id for a pointer. */
		fid: (ptr: string) => string;
		disabled?: boolean;
	} = $props();

	const names = $derived(Object.keys(spec.operations ?? {}));
	let nameErrors = $state<Record<string, string>>({});
	let announce = $state('');

	function rename(from: string, raw: string) {
		const to = raw.trim();
		const problem = operationNameProblem(spec, to, from);
		if (problem) {
			nameErrors[from] = problem;
			return;
		}
		delete nameErrors[from];
		renameOperation(spec, from, to);
		if (from !== to) announce = `Renamed ${from} to ${to}.`;
	}
	function add() {
		const name = addOperation(spec);
		announce = `Added operation ${name}. Describe its request below and give targets a pair for it if it goes to another endpoint.`;
	}
	function remove(name: string) {
		removeOperation(spec, name);
		announce = `Removed operation ${name}.`;
	}
</script>

<fieldset class="fk-group" {disabled} aria-describedby="{fid('/operations')}-hint">
	<legend>Operations</legend>
	<p id="{fid('/operations')}-hint" class="fk-hint">
		What callers invoke (<code
			>POST /v1/integrations/&lbrace;id&rbrace;/invoke/&lbrace;operation&rbrace;</code
		>) and what they must send. A single-operation integration uses
		<code>{DEFAULT_OPERATION}</code>{interactive
			? '. This integration is interactive: its operations are steps of one session.'
			: '.'}
	</p>
	<FieldError id="{fid('/operations')}-error" message={at('/operations')} />
	<ol class="ops">
		{#each names as name (name)}
			{@const op = spec.operations[name]!}
			{@const o = `/operations/${escapeToken(name)}`}
			<li class="op" aria-labelledby="{fid(o)}-h">
				<div class="head">
					<h3 id="{fid(o)}-h">Operation <code>{name}</code></h3>
					<button
						type="button"
						class="btn btn-quiet"
						disabled={names.length === 1}
						onclick={() => remove(name)}
						aria-label="Remove operation {name}"><Icon name="close" size={14} />Remove</button
					>
				</div>
				<FieldError id="{fid(o)}-error" message={at(o)} />
				<div class="fk-grid">
					<div class="fk-field">
						<label for="{fid(o)}-name">Name</label>
						<input
							id="{fid(o)}-name"
							value={name}
							onchange={(e) => rename(name, e.currentTarget.value)}
							onkeydown={(e) => {
								// Enter renames; it must not submit the whole draft.
								if (e.key !== 'Enter') return;
								e.preventDefault();
								rename(name, e.currentTarget.value);
							}}
							maxlength="32"
							autocomplete="off"
							spellcheck="false"
							aria-invalid={nameErrors[name] ? 'true' : undefined}
							aria-describedby="{fid(o)}-name-hint{nameErrors[name] ? ` ${fid(o)}-name-error` : ''}"
						/>
						<p id="{fid(o)}-name-hint" class="fk-hint">
							Callers put it in the URL. Applied on Enter or leaving the field.
						</p>
						<FieldError id="{fid(o)}-name-error" message={nameErrors[name]} />
					</div>
					<div class="fk-field wide">
						<label for={fid(`${o}/description`)}
							>Description <span class="fk-req">(shown to callers)</span></label
						>
						<input
							id={fid(`${o}/description`)}
							value={op.description ?? ''}
							oninput={(e) => {
								const v = e.currentTarget.value;
								if (v.trim()) op.description = v;
								else delete op.description;
							}}
							maxlength="1024"
							autocomplete="off"
							placeholder="Estimate the age for a first and last name."
							aria-invalid={at(`${o}/description`) ? 'true' : undefined}
							aria-describedby="{fid(`${o}/description`)}-hint{at(`${o}/description`)
								? ` ${fid(`${o}/description`)}-error`
								: ''}"
						/>
						<p id="{fid(`${o}/description`)}-hint" class="fk-hint">
							No vendor, account or version in it: callers read it in discovery.
						</p>
						<FieldError id="{fid(`${o}/description`)}-error" message={at(`${o}/description`)} />
					</div>
					<div class="fk-field wide">
						<label class="fk-check">
							<input
								type="checkbox"
								checked={op.idempotent}
								onchange={(e) => (op.idempotent = e.currentTarget.checked)}
								aria-describedby="{fid(`${o}/idempotent`)}-hint"
							/>
							Idempotent: repeating the call has no extra side effect
						</label>
						<p id="{fid(`${o}/idempotent`)}-hint" class="fk-hint">
							Allows an immediate retry on timeout and failover without an Idempotency-Key.
						</p>
						<FieldError id="{fid(`${o}/idempotent`)}-error" message={at(`${o}/idempotent`)} />
					</div>
					<div class="fk-field wide">
						<label class="fk-check switch">
							<input
								type="checkbox"
								role="switch"
								checked={op.validate_input ?? true}
								onchange={(e) => (op.validate_input = e.currentTarget.checked)}
								aria-describedby="{fid(`${o}/validate_input`)}-hint"
							/>
							Check live calls against the request schema:
							<strong>{(op.validate_input ?? true) ? 'On' : 'Off'}</strong>
						</label>
						<p id="{fid(`${o}/validate_input`)}-hint" class="fk-hint">
							On (default): a call that misses a required field or sends a wrong type gets 400
							VALIDATION_FAILED before anything else runs. Off: live calls are not checked; the
							samples are still checked at publish.
						</p>
						<FieldError
							id="{fid(`${o}/validate_input`)}-error"
							message={at(`${o}/validate_input`)}
						/>
					</div>
				</div>
				<SchemaEditor
					label="Request body"
					hint="The caller's JSON body. The request transformer reads it as req.* (req.firstName)."
					value={op.request_schema.body}
					error={at(`${o}/request_schema/body`) ?? at(`${o}/request_schema`)}
					onchange={(s) => (op.request_schema.body = s ?? { type: 'object' })}
				/>
				<SchemaEditor
					label="Request headers"
					noun="header"
					optional
					hint="Caller headers the request transformer may read as hdr.* (hdr.X-Request-Source); others stay invisible to it. Authorization, Idempotency-Key, Prefer, X-Request-Deadline, X-Hub-Session and traceparent belong to the Hub and cannot be declared."
					value={op.request_schema.headers}
					error={at(`${o}/request_schema/headers`)}
					onchange={(s) => {
						if (s) op.request_schema.headers = s;
						else delete op.request_schema.headers;
					}}
				/>
			</li>
		{/each}
	</ol>
	<div><button type="button" class="btn" onclick={add}>Add operation</button></div>
	<p class="visually-hidden" role="status">{announce}</p>
</fieldset>

<style>
	.ops {
		display: grid;
		gap: var(--space-3);
		margin: 0;
		padding: 0;
		list-style: none;
	}
	.op {
		display: grid;
		gap: var(--space-3);
		padding: var(--space-3);
		border: 1px solid var(--rule);
		border-left: 3px solid var(--signal);
		border-radius: var(--radius-sm);
		background: var(--paper);
		min-width: 0;
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
	}
	h3 {
		margin: 0;
		font-size: var(--text-md);
	}
	.switch strong {
		margin-left: 0.2rem;
	}
</style>

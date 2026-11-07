<script lang="ts">
	import { untrack } from 'svelte';
	import {
		FIELD_TYPES,
		fieldsHolder,
		freshName,
		fromJsonSchema,
		inferFromSample,
		mergeInferred,
		newNode,
		schemaDocumentProblem,
		setType,
		toJsonSchema,
		treeProblems,
		typeLabels,
		type FieldType,
		type SchemaNode,
		type SchemaProperty
	} from '$lib/utils/json-schema';
	import { parseJsonObject } from '$lib/utils/json-input';
	import FieldError from '../FieldError.svelte';
	import Icon from '../Icon.svelte';

	/**
	 * Schema registry editor (HUB-436, ADR-0023): one JSON Schema 2020-12 document edited field by
	 * field. Paste a sample JSON and the fields are generated (name, type, nested objects and
	 * arrays); tick each one required or optional, describe it, add or remove fields. "JSON
	 * Schema" shows the same document as text for power users; edits there are checked and flow
	 * back into the field list. Keywords without a column (`minLength`, `pattern`, `x-pii`, ...)
	 * are listed on their field and kept as they are.
	 *
	 * It emits only when the operator changes something (never on mount), through `onchange`:
	 * the document, or null for an optional part left empty ("not sent").
	 */
	type Doc = Record<string, unknown>;
	let {
		value,
		label,
		hint,
		optional = false,
		noun = 'field',
		error = null,
		disabled = false,
		onchange
	}: {
		/** The current document; null/undefined = nothing declared. */
		value: Doc | null | undefined;
		/** "Body", "Headers", "Query parameters". */
		label: string;
		hint?: string;
		/** Empty means "not sent" and emits null; otherwise empty emits `{type: object}`. */
		optional?: boolean;
		/** What one top-level entry is called: "field", "header", "query parameter". */
		noun?: string;
		/** A problem from the API at this document's pointer. */
		error?: string | null;
		disabled?: boolean;
		onchange: (schema: Doc | null) => void;
	} = $props();

	const uid = $props.id();
	const rootOf = (v: Doc | null | undefined): SchemaNode => {
		const n = fromJsonSchema(v ?? { type: 'object' });
		if (n.type === 'any' && !n.properties.length && !Object.keys(n.extra).length) n.type = 'object';
		return n;
	};
	const key = (v: unknown) => JSON.stringify(v ?? null);

	let view = $state<'fields' | 'json'>('fields');
	let jsonText = $state('');
	let jsonError = $state<string | null>(null);
	const pretty = (v: unknown) => JSON.stringify(v, null, 2);

	let root = $state<SchemaNode>(untrack(() => rootOf(value)));
	let seen = untrack(() => key(value));
	// The parent replaced the document (a prefill, a YAML edit): show the new one.
	$effect.pre(() => {
		const k = key(value);
		if (k === seen) return;
		seen = k;
		root = rootOf(value);
		if (view === 'json') jsonText = pretty(value ?? toJsonSchema(root));
	});

	const problems = $derived(treeProblems(root));
	const isObjectRoot = $derived(root.type === 'object');
	const fieldCount = $derived(countFields(root));
	let announce = $state('');

	function countFields(n: SchemaNode): number {
		let c = 0;
		for (const p of n.properties) c += 1 + countFields(p.node);
		if (n.items) c += countFields(n.items);
		return c;
	}

	function emptyRoot(n: SchemaNode): boolean {
		return (
			(n.type === 'object' || n.type === 'any') &&
			!n.properties.length &&
			!Object.keys(n.extra).length &&
			!n.description.trim()
		);
	}

	function emit(doc?: Doc) {
		const out: Doc | null =
			doc ?? (emptyRoot(root) ? (optional ? null : { type: 'object' }) : toJsonSchema(root));
		seen = key(out);
		onchange(out);
	}

	// ---- field edits ----------------------------------------------------------------------------
	function addField(holder: SchemaNode, what = 'field') {
		if (holder.type === 'any') holder.type = 'object';
		const name = freshName(
			holder.properties,
			what === 'field' ? 'field' : what.replace(/\W+/g, '_')
		);
		holder.properties.push({ name, required: false, node: newNode('string') });
		emit();
	}
	function removeField(holder: SchemaNode, i: number, path: string) {
		holder.properties.splice(i, 1);
		announce = `Removed ${path}.`;
		emit();
	}
	function rename(p: SchemaProperty, name: string) {
		p.name = name;
		emit();
	}
	function retype(n: SchemaNode, type: FieldType) {
		setType(n, type);
		emit();
	}

	// ---- sample ---------------------------------------------------------------------------------
	let sampleOpen = $state(false);
	let sampleText = $state('');
	let sampleError = $state<string | null>(null);
	function generate() {
		const parsed = parseJsonObject(sampleText, { what: 'The sample' });
		if (!parsed.ok) {
			sampleError = parsed.message;
			return;
		}
		sampleError = null;
		root = mergeInferred(inferFromSample(parsed.value ?? {}), root);
		const n = countFields(root);
		announce = `Generated ${n} ${n === 1 ? 'field' : 'fields'} from the sample. New fields start as required: untick the optional ones.`;
		sampleOpen = false;
		emit();
	}

	// ---- JSON view ------------------------------------------------------------------------------
	function showJson() {
		jsonText = pretty(emptyRoot(root) && optional ? {} : toJsonSchema(root));
		jsonError = null;
		view = 'json';
	}
	function showFields() {
		if (jsonError) return;
		view = 'fields';
	}
	function onJson(text: string) {
		jsonText = text;
		if (!text.trim()) {
			jsonError = null;
			root = rootOf(null);
			emit(optional ? undefined : { type: 'object' });
			return;
		}
		const parsed = parseJsonObject(text, { what: 'The JSON Schema' });
		if (!parsed.ok) {
			jsonError = parsed.message;
			return;
		}
		const problem = schemaDocumentProblem(parsed.value);
		if (problem) {
			jsonError = problem[0]!.toUpperCase() + problem.slice(1);
			return;
		}
		jsonError = null;
		root = rootOf(parsed.value);
		emit(emptyRoot(root) ? undefined : parsed.value);
	}

	/** Enter in a field of the editor must not submit the form around it. */
	function keepEnter(e: KeyboardEvent) {
		const t = e.target as HTMLElement;
		if (e.key === 'Enter' && t instanceof HTMLInputElement && t.type !== 'checkbox')
			e.preventDefault();
	}

	const extrasOf = (n: SchemaNode) =>
		Object.entries(n.extra).map(([k, v]) =>
			typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean' ? `${k} ${v}` : k
		);
	const nounPlural = $derived(noun.endsWith('s') ? noun : `${noun}s`);
</script>

{#snippet typeSelect(n: SchemaNode, name: string)}
	<select
		class="type"
		value={n.type}
		onchange={(e) => retype(n, e.currentTarget.value as FieldType)}
		aria-label="Type of {name}"
	>
		{#each FIELD_TYPES as t (t)}<option value={t}>{typeLabels[t]}</option>{/each}
	</select>
	{#if n.nullable}<span class="or-null">or null</span>{/if}
{/snippet}

{#snippet extras(n: SchemaNode)}
	{@const list = extrasOf(n)}
	{#if list.length}
		<span class="extras" title="Edit these in the JSON Schema view">{list.join(' · ')}</span>
	{/if}
{/snippet}

{#snippet rows(holder: SchemaNode, prefix: string, depth: number)}
	{#each holder.properties as p, i (p.node.id)}
		{@const path = `${prefix}${p.name || '(unnamed)'}`}
		{@const inner = fieldsHolder(p.node)}
		<tr>
			<th scope="row" class="name" style="--depth: {depth}">
				{#if depth}<span class="branch" aria-hidden="true">└</span>{/if}
				<input
					value={p.name}
					oninput={(e) => rename(p, e.currentTarget.value)}
					aria-label="Name of {path}"
					aria-invalid={problems.some((x) => x.path === path) ? 'true' : undefined}
					autocomplete="off"
					spellcheck="false"
				/>
			</th>
			<td>{@render typeSelect(p.node, path)}</td>
			<td>
				<label class="req">
					<input
						type="checkbox"
						checked={p.required}
						onchange={(e) => {
							p.required = e.currentTarget.checked;
							emit();
						}}
						aria-label="{path} required"
					/>
					<span aria-hidden="true">{p.required ? 'Required' : 'Optional'}</span>
				</label>
			</td>
			<td class="desc">
				<input
					value={p.node.description}
					oninput={(e) => {
						p.node.description = e.currentTarget.value;
						emit();
					}}
					aria-label="Description of {path}"
					placeholder="optional"
					autocomplete="off"
				/>
				{@render extras(p.node)}
			</td>
			<td class="acts">
				{#if inner}
					<button
						type="button"
						class="btn btn-quiet"
						onclick={() => addField(inner)}
						aria-label="Add a field inside {path}">+ Field</button
					>
				{/if}
				<button
					type="button"
					class="btn btn-quiet"
					onclick={() => removeField(holder, i, path)}
					aria-label="Remove {path}"><Icon name="close" size={12} />Remove</button
				>
			</td>
		</tr>
		{#if p.node.type === 'array' && p.node.items}
			{@const items = p.node.items}
			<tr class="items">
				<th scope="row" class="name" style="--depth: {depth + 1}">
					<span class="branch" aria-hidden="true">└</span><code>{path}[]</code>
					<span class="each">each item</span>
				</th>
				<td>{@render typeSelect(items, `${path}[]`)}</td>
				<td><span class="muted">-</span></td>
				<td class="desc">
					<input
						value={items.description}
						oninput={(e) => {
							items.description = e.currentTarget.value;
							emit();
						}}
						aria-label="Description of {path}[]"
						placeholder="optional"
						autocomplete="off"
					/>
					{@render extras(items)}
				</td>
				<td class="acts"></td>
			</tr>
			{#if items.type === 'object'}{@render rows(items, `${path}[].`, depth + 2)}{/if}
		{:else if p.node.type === 'object'}
			{@render rows(p.node, `${path}.`, depth + 1)}
		{/if}
	{/each}
{/snippet}

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<fieldset
	class="schema"
	{disabled}
	aria-describedby="{hint ? `${uid}-hint ` : ''}{uid}-count"
	onkeydown={keepEnter}
>
	<legend>
		{label}
		<span class="fk-req">{optional ? '(optional: empty = not sent)' : '(required)'}</span>
	</legend>
	{#if hint}<p id="{uid}-hint" class="fk-hint">{hint}</p>{/if}

	<div class="bar">
		<div class="views" role="group" aria-label="View of {label}">
			<button
				type="button"
				class="btn btn-quiet"
				aria-pressed={view === 'fields'}
				onclick={showFields}>Fields</button
			>
			<button type="button" class="btn btn-quiet" aria-pressed={view === 'json'} onclick={showJson}
				>Edit as JSON Schema</button
			>
		</div>
		<span id="{uid}-count" class="count">
			{fieldCount === 0
				? optional
					? 'Nothing declared: not sent.'
					: `No ${nounPlural} declared.`
				: `${fieldCount} ${fieldCount === 1 ? 'field' : 'fields'}`}
		</span>
		{#if view === 'fields'}
			<button
				type="button"
				class="btn btn-quiet"
				aria-expanded={sampleOpen}
				aria-controls="{uid}-sample"
				onclick={() => (sampleOpen = !sampleOpen)}>Paste a sample</button
			>
		{/if}
	</div>

	{#if view === 'fields'}
		<div id="{uid}-sample" class="sample" hidden={!sampleOpen}>
			<label for="{uid}-sample-text" class="fk-label">Sample {label.toLowerCase()} (JSON)</label>
			<textarea
				id="{uid}-sample-text"
				class="fk-input fk-mono"
				rows="5"
				bind:value={sampleText}
				spellcheck="false"
				placeholder={'{ "firstName": "Mich", "lastName": "ael" }'}
				aria-invalid={sampleError ? 'true' : undefined}
				aria-describedby="{uid}-sample-hint{sampleError ? ` ${uid}-sample-error` : ''}"></textarea>
			<p id="{uid}-sample-hint" class="fk-hint">
				Every {noun} found becomes a field with its type, nested objects and arrays included. New fields
				start as required; fields you already have keep their settings.
			</p>
			<FieldError id="{uid}-sample-error" message={sampleError} />
			<div><button type="button" class="btn" onclick={generate}>Generate fields</button></div>
		</div>

		{#if isObjectRoot || root.type === 'any'}
			{#if root.properties.length}
				<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
				<div class="scroll" role="region" aria-label="{label} fields" tabindex="0">
					<table>
						<caption class="visually-hidden"
							>{label}: fields, types and required or optional</caption
						>
						<thead>
							<tr>
								<th scope="col">Field</th>
								<th scope="col">Type</th>
								<th scope="col">Required</th>
								<th scope="col">Description</th>
								<th scope="col"><span class="visually-hidden">Actions</span></th>
							</tr>
						</thead>
						<tbody>{@render rows(root, '', 0)}</tbody>
					</table>
				</div>
			{:else}
				<p class="empty">
					{optional ? `No ${nounPlural}: nothing is sent.` : `No ${nounPlural} yet.`} Paste a sample or
					add one.
				</p>
			{/if}
			{#if problems.length}
				<ul class="problems" role="alert">
					{#each problems as pr, i (i)}<li><code>{pr.path}</code>: {pr.message}</li>{/each}
				</ul>
			{/if}
			<div class="foot">
				<button type="button" class="btn btn-quiet" onclick={() => addField(root, noun)}
					>+ Add {noun}</button
				>
			</div>
		{:else}
			<p class="fk-note" role="note">
				The {label.toLowerCase()} is a {root.type}, not an object: edit it in the JSON Schema view.
			</p>
		{/if}
	{:else}
		<div class="fk-field">
			<label for="{uid}-json" class="visually-hidden">{label} as JSON Schema</label>
			<textarea
				id="{uid}-json"
				class="fk-input fk-mono"
				rows={Math.min(24, Math.max(6, jsonText.split('\n').length + 1))}
				value={jsonText}
				oninput={(e) => onJson(e.currentTarget.value)}
				spellcheck="false"
				aria-invalid={jsonError ? 'true' : undefined}
				aria-describedby="{uid}-json-hint{jsonError ? ` ${uid}-json-error` : ''}"></textarea>
			<p id="{uid}-json-hint" class="fk-hint">
				JSON Schema 2020-12. Fields listed in <code>required</code> are required, every other
				declared field is optional.{optional ? ' Empty or {} means nothing is sent.' : ''}
			</p>
			<FieldError id="{uid}-json-error" message={jsonError} />
			{#if jsonError}<p class="fk-hint">Fix it to go back to the field list.</p>{/if}
		</div>
	{/if}
	<FieldError id="{uid}-error" message={error} />
	<p class="visually-hidden" role="status">{announce}</p>
</fieldset>

<style>
	.schema {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding: var(--space-3);
		border: 1px solid var(--rule);
		border-left: 3px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		min-width: 0;
	}
	legend {
		padding: 0 var(--space-1);
		font-size: var(--text-sm);
		font-weight: 700;
	}
	.bar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}
	.views {
		display: inline-flex;
	}
	.views .btn + .btn {
		margin-left: -1px;
	}
	.views [aria-pressed='true'] {
		border-color: var(--signal);
		background: var(--signal-wash);
	}
	.count {
		color: var(--ink-muted);
		font-size: var(--text-xs);
		margin-right: auto;
	}
	.sample {
		display: grid;
		gap: 0.25rem;
		padding: var(--space-2) var(--space-3);
		border: 1px dashed var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--paper);
	}
	.sample[hidden] {
		display: none;
	}
	.scroll {
		position: relative;
		overflow-x: auto;
		border: 1px solid var(--rule);
		border-radius: var(--radius-sm);
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: var(--text-sm);
	}
	th,
	td {
		height: var(--row-height);
		padding: 0.15rem var(--space-2);
		text-align: left;
		vertical-align: middle;
		border-bottom: 1px solid var(--rule);
	}
	thead th {
		background: var(--surface-sunken);
		color: var(--ink-muted);
		font-size: var(--text-xs);
		font-weight: 600;
		white-space: nowrap;
	}
	tbody tr:last-child > * {
		border-bottom: 0;
	}
	.name {
		padding-left: calc(var(--space-2) + var(--depth, 0) * 1.1rem);
		font-weight: 400;
		white-space: nowrap;
	}
	.branch {
		margin-right: 0.3rem;
		color: var(--ink-muted);
	}
	input:not([type='checkbox']),
	select {
		min-height: 1.75rem;
		padding: 0.1rem 0.4rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		color: var(--ink);
		font: inherit;
		font-size: var(--text-sm);
	}
	.name input {
		width: 11rem;
		font-family: var(--font-id);
	}
	[aria-invalid='true'] {
		border-color: var(--status-danger);
	}
	.desc input {
		width: 100%;
		min-width: 9rem;
	}
	.type {
		min-width: 6.5rem;
	}
	.or-null,
	.each,
	.extras {
		margin-left: 0.3rem;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.extras {
		display: block;
		margin: 0.1rem 0 0;
		font-family: var(--font-id);
		overflow-wrap: anywhere;
	}
	.items .name code {
		font-size: var(--text-xs);
	}
	.req {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		white-space: nowrap;
	}
	.req input {
		width: 1rem;
		height: 1rem;
		accent-color: var(--signal);
	}
	.acts {
		white-space: nowrap;
		text-align: right;
	}
	.empty {
		margin: 0;
		padding: var(--space-2) var(--space-3);
		border: 1px dashed var(--rule-strong);
		border-radius: var(--radius-sm);
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.problems {
		margin: 0;
		padding-left: var(--space-4);
		color: var(--status-danger);
		font-size: var(--text-sm);
		font-weight: 600;
	}
	.foot {
		display: flex;
		gap: var(--space-2);
	}
</style>

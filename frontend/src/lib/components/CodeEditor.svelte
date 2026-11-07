<script lang="ts" module>
	/** A problem to mark: a JSON pointer into the YAML document, or a line number, plus a message. */
	export interface CodeProblem {
		pointer?: string;
		line?: number | null;
		message: string;
	}
</script>

<script lang="ts">
	import Icon from '$lib/components/Icon.svelte';
	import { pointerLine, pointerTokens } from '$lib/utils/spec-yaml';
	import { pointerToPath } from '$lib/utils/diff';

	/**
	 * Plain-text code editor (integration specs HUB-259, transformer sources HUB-293): a textarea
	 * without wrapping and a line-number gutter. Problems (a JSON pointer into the YAML, or a line)
	 * are listed under it with their line and a "Go to line" button, and their lines are marked in
	 * the gutter.
	 */
	let {
		value = $bindable(''),
		label,
		labelNote = '',
		name,
		hint,
		problemsTitle,
		rows = 22,
		problems = [],
		error = null,
		disabled = false
	}: {
		value?: string;
		label: string;
		/** Muted text after the label, e.g. "(YAML or JSON)". */
		labelNote?: string;
		/** Form field name. */
		name: string;
		hint: string;
		/** Heading of the problems list. */
		problemsTitle: string;
		rows?: number;
		problems?: CodeProblem[];
		/** A whole-field problem (YAML syntax), e.g. "Line 3: ...". */
		error?: string | null;
		disabled?: boolean;
	} = $props();

	const id = $props.id();
	let textarea = $state<HTMLTextAreaElement>();
	let gutter = $state<HTMLElement>();

	const lineCount = $derived(Math.max(1, value.split('\n').length));
	const located = $derived(
		problems.map((p) => ({
			...p,
			pointer: p.pointer ?? '',
			line: p.line ?? (p.pointer !== undefined ? pointerLine(value, p.pointer) : null),
			path: p.pointer !== undefined ? pointerToPath(p.pointer) : ''
		}))
	);
	const badLines = $derived(new Set(located.map((p) => p.line).filter((l) => l !== null)));
	const invalid = $derived(!!error || problems.length > 0);

	function syncScroll() {
		if (gutter && textarea) gutter.scrollTop = textarea.scrollTop;
	}

	function goToLine(line: number) {
		if (!textarea) return;
		const lines = value.split('\n');
		const start = lines.slice(0, line - 1).reduce((n, l) => n + l.length + 1, 0);
		const end = start + (lines[line - 1]?.length ?? 0);
		textarea.focus();
		textarea.setSelectionRange(start, end);
		const lh = parseFloat(getComputedStyle(textarea).lineHeight) || 20;
		textarea.scrollTop = Math.max(0, (line - 3) * lh);
		syncScroll();
	}

	// Pointers whose path does not exist yet (a missing property) are reported at their parent.
	const where = (p: { pointer: string; line: number | null }) =>
		p.line === null ? 'Not found in the text' : `Line ${p.line}`;
	const depth = (pointer: string) => pointerTokens(pointer).length;
</script>

<div class="editor" style:--rows={rows}>
	<label for="{id}-code"
		>{label}{#if labelNote}&nbsp;<span class="muted">{labelNote}</span>{/if}</label
	>
	<div class="frame" class:invalid>
		<div class="gutter" bind:this={gutter} aria-hidden="true">
			{#each { length: lineCount }, i (i)}
				<span class:bad={badLines.has(i + 1)}>{i + 1}</span>
			{/each}
		</div>
		<textarea
			bind:this={textarea}
			id="{id}-code"
			{name}
			bind:value
			onscroll={syncScroll}
			wrap="off"
			spellcheck="false"
			autocomplete="off"
			autocapitalize="off"
			{rows}
			{disabled}
			aria-invalid={invalid ? 'true' : undefined}
			aria-describedby="{id}-hint{invalid ? ` ${id}-problems` : ''}"></textarea>
	</div>
	<p id="{id}-hint" class="hint">{hint}</p>
	{#if invalid}
		<div id="{id}-problems" class="problems" role="alert">
			<p class="title"><Icon name="alert" size={16} />{problemsTitle}</p>
			<ul>
				{#if error}<li>{error}</li>{/if}
				{#each located as p, i (i)}
					<li>
						<span class="where">{where(p)}</span>
						{#if p.pointer}<code title={p.pointer}>{depth(p.pointer) ? p.path : name}</code>{/if}
						<span>{p.message}</span>
						{#if p.line !== null}
							<button type="button" onclick={() => goToLine(p.line!)}>Go to line {p.line}</button>
						{/if}
					</li>
				{/each}
			</ul>
		</div>
	{/if}
</div>

<style>
	.editor {
		display: grid;
		gap: 0.3rem;
	}
	label {
		font-weight: 600;
		font-size: var(--text-sm);
	}
	.frame {
		display: grid;
		grid-template-columns: auto 1fr;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		overflow: hidden;
	}
	.frame.invalid {
		border-color: var(--status-danger);
	}
	.frame:focus-within {
		box-shadow: var(--focus-ring);
	}
	.gutter,
	textarea {
		font-family: var(--font-id);
		font-size: var(--text-sm);
		line-height: 1.5;
		padding: 0.5rem 0.6rem;
	}
	.gutter {
		display: flex;
		flex-direction: column;
		overflow: hidden;
		max-height: calc(var(--rows) * 1.5em + 1rem);
		background: var(--surface-sunken);
		color: var(--ink-muted);
		text-align: right;
		user-select: none;
	}
	.gutter .bad {
		color: var(--status-danger);
		font-weight: 700;
	}
	.gutter .bad::before {
		content: '! ';
	}
	textarea {
		width: 100%;
		height: calc(var(--rows) * 1.5em + 1rem);
		border: 0;
		resize: vertical;
		background: transparent;
		white-space: pre;
		overflow: auto;
		tab-size: 2;
	}
	textarea:focus-visible {
		box-shadow: none;
	}
	.hint {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.problems {
		padding: var(--space-3);
		border: 1px solid var(--status-danger);
		border-left-width: 4px;
		border-radius: var(--radius-md);
		background: var(--status-danger-wash);
		font-size: var(--text-sm);
	}
	.title {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		margin: 0 0 var(--space-2);
		font-weight: 700;
	}
	ul {
		display: grid;
		gap: var(--space-1);
		margin: 0;
		padding-left: var(--space-4);
	}
	li {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}
	.where {
		font-weight: 700;
	}
	li button {
		padding: 0.05rem 0.5rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-xs);
		font-weight: 700;
		cursor: pointer;
	}
</style>

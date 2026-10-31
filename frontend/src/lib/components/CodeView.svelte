<script lang="ts">
	import { highlightYaml } from '$lib/utils/yaml-highlight';

	/**
	 * Read-only YAML with line numbers and syntax colours (HUB-268). Tokens are rendered as text,
	 * never as HTML. Colour is decoration only: the text itself is unchanged. Focusable so the
	 * scrollable block can be read by keyboard.
	 */
	let { source, label }: { source: string; label: string } = $props();

	const lines = $derived(highlightYaml(source));
	const width = $derived(String(lines.length).length);
	/** Keeps line breaks when the source is copied. */
	const NL = '\n';
</script>

<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<pre class="code" tabindex="0" aria-label={label} style:--ln-width="{width}ch"><code
		>{#each lines as tokens, i (i)}<span class="line"
				><span class="ln" aria-hidden="true">{i + 1}</span>{#each tokens as t, j (j)}<span
						class="t-{t.kind}">{t.text}</span
					>{/each}{NL}</span
			>{/each}</code
	></pre>

<style>
	.code {
		max-height: 32rem;
		overflow: auto;
		margin: 0;
		padding: var(--space-2) 0;
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
		font-family: var(--font-id);
		font-size: var(--text-sm);
		line-height: 1.55;
	}
	.line {
		display: block;
		padding-right: var(--space-3);
	}
	.ln {
		display: inline-block;
		width: calc(var(--ln-width) + 1.5rem);
		padding-right: var(--space-3);
		margin-right: var(--space-2);
		border-right: 1px solid var(--rule);
		color: var(--ink-muted);
		text-align: right;
		user-select: none;
	}
	.t-key {
		color: var(--signal);
		font-weight: 700;
	}
	.t-string {
		color: var(--status-ok);
	}
	.t-number,
	.t-literal {
		color: var(--status-warn);
	}
	.t-comment {
		color: var(--ink-muted);
		font-style: italic;
	}
	.t-punct {
		color: var(--ink-muted);
	}
</style>

<script lang="ts">
	import Icon from './Icon.svelte';

	let { value, label }: { value: string; /** e.g. "Copy request id" */ label: string } = $props();

	let copied = $state(false);
	let timer: ReturnType<typeof setTimeout> | undefined;

	async function copy() {
		try {
			await navigator.clipboard.writeText(value);
			copied = true;
			clearTimeout(timer);
			timer = setTimeout(() => (copied = false), 2000);
		} catch {
			copied = false;
		}
	}
</script>

<button type="button" class="copy" onclick={copy} aria-label={label} title={label}>
	<Icon name={copied ? 'check' : 'copy'} size={16} />
</button>
<span class="visually-hidden" aria-live="polite">{copied ? 'Copied' : ''}</span>

<style>
	.copy {
		display: inline-grid;
		place-items: center;
		width: 1.75rem;
		height: 1.75rem;
		padding: 0;
		border: 1px solid var(--rule);
		border-radius: var(--radius-sm);
		background: var(--surface);
		color: var(--ink-muted);
		cursor: pointer;
	}
	.copy:hover {
		color: var(--ink);
		border-color: var(--rule-strong);
	}
</style>

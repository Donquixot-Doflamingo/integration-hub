<script lang="ts">
	import Icon from './Icon.svelte';
	import type { IconName } from './icons';

	/** State is always text + icon; the tone colour is only a third cue (WCAG 1.4.1). */
	let {
		tone,
		icon,
		label
	}: { tone: 'ok' | 'warn' | 'danger' | 'neutral'; icon: IconName; label: string } = $props();
</script>

<span class="badge {tone}"><Icon name={icon} size={14} />{label}</span>

<style>
	.badge {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		padding: 0.05rem 0.45rem 0.05rem 0.3rem;
		border-radius: 999px;
		font-size: var(--text-xs);
		font-weight: 700;
		line-height: 1.5;
		border: 1px solid currentColor;
	}
	.ok {
		color: var(--status-ok);
		background: var(--status-ok-wash);
	}
	.warn {
		color: var(--status-warn);
		background: var(--status-warn-wash);
	}
	.danger {
		color: var(--status-danger);
		background: var(--status-danger-wash);
	}
	.neutral {
		color: var(--status-neutral);
		background: var(--status-neutral-wash);
	}
</style>

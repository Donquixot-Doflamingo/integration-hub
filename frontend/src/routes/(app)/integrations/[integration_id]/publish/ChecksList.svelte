<script lang="ts">
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import type { StatusView } from '$lib/status';
	import type { ValidationReport } from '$lib/api/types';
	import { pointerToPath } from '$lib/utils/diff';
	import { checkLabel } from './draft';

	/**
	 * The publish checks of DESIGN §6.4 (`ValidationReport`), each with its result in text + icon.
	 * Sample checks (`input_schema`, `goldens_pass`, `endpoint_schema`, HUB-436) also name the
	 * sample and the field that broke.
	 */
	let { report, heading = 'Publish checks' }: { report: ValidationReport; heading?: string } =
		$props();

	const status: Record<string, StatusView> = {
		pass: { tone: 'ok', icon: 'check', label: 'Pass' },
		fail: { tone: 'danger', icon: 'open', label: 'Fail' },
		warn: { tone: 'warn', icon: 'alert', label: 'Warning' },
		skipped: { tone: 'neutral', icon: 'changelog', label: 'Skipped' }
	};
	const failed = $derived(report.checks.filter((c) => c.status === 'fail').length);
	const summary = $derived(
		report.valid
			? `All ${report.checks.length} checks pass. The draft can be published.`
			: `${failed} of ${report.checks.length} checks fail. Fix them before publishing.`
	);
	const id = $props.id();
</script>

<section class="checks" aria-labelledby="{id}-h">
	<h3 id="{id}-h">{heading}</h3>
	<p class="summary" role="status">{summary}</p>
	<ul>
		{#each report.checks as c, i (i)}
			{@const s = status[c.status] ?? status.skipped!}
			<li>
				<StatusBadge tone={s.tone} icon={s.icon} label={s.label} />
				<span class="name">{checkLabel(c.check)}</span>
				{#if c.sample}<span class="where">sample <code>{c.sample}</code></span>{/if}
				{#if c.field}<span class="where">field <code>{c.field}</code></span>{/if}
				{#if c.message}<span class="msg">{c.message}</span>{/if}
				{#if c.pointer}<code title={c.pointer}>{pointerToPath(c.pointer)}</code>{/if}
			</li>
		{/each}
	</ul>
</section>

<style>
	.checks {
		display: grid;
		gap: var(--space-2);
	}
	h3 {
		font-size: var(--text-md);
	}
	.summary {
		margin: 0;
		font-size: var(--text-sm);
		font-weight: 600;
	}
	ul {
		display: grid;
		gap: var(--space-1);
		margin: 0;
		padding: 0;
		list-style: none;
		font-size: var(--text-sm);
	}
	li {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}
	.msg {
		color: var(--ink-muted);
	}
	.where {
		font-weight: 600;
	}
</style>

<script lang="ts">
	import type { SubmitFunction } from '@sveltejs/kit';
	import { applyAction, enhance } from '$app/forms';
	import { fromPlainError, type ActionFailureData, type ActionSuccessData } from '$lib/api/actions';
	import { HubApiError } from '$lib/api/errors';
	import type { GoldensRunResult } from '$lib/api/types';
	import ErrorPanel from '../ErrorPanel.svelte';
	import Icon from '../Icon.svelte';
	import GoldensResult from './GoldensResult.svelte';

	/**
	 * "Run goldens" for one stored version (HUB-268): posts `ref` to the `runGoldens` form action
	 * (`runTransformerGoldens`, editor role, non-mutating) and shows the per-fixture result. The API
	 * answers 200 whether fixtures pass or fail; failures of the call itself show code and request id.
	 * The parent keys it by `ref`, so another version starts with a clean result.
	 */
	let { transformerRef }: { transformerRef: string } = $props();

	let running = $state(false);
	let result = $state.raw<GoldensRunResult | null>(null);
	let error = $state.raw<HubApiError | null>(null);

	const submit: SubmitFunction = ({ cancel }) => {
		if (running) return cancel();
		running = true;
		error = null;
		return async ({ result: r }) => {
			try {
				if (r.type === 'success') {
					result = (r.data as ActionSuccessData<GoldensRunResult>).result;
				} else if (r.type === 'failure') {
					const d = r.data as ActionFailureData | undefined;
					error = d?.error
						? fromPlainError(d.error)
						: new HubApiError({
								status: r.status,
								code: 'VALIDATION_FAILED',
								message: d?.fields?.ref ?? 'The samples could not run.'
							});
				} else if (r.type === 'redirect') {
					await applyAction(r);
				} else {
					error = new HubApiError({
						status: r.type === 'error' ? (r.status ?? 500) : 500,
						code: 'UNEXPECTED_RESPONSE',
						message: 'The console could not run the samples. Try again.'
					});
				}
			} finally {
				running = false;
			}
		};
	};
</script>

<form method="POST" action="?/runGoldens" use:enhance={submit} class="run">
	<input type="hidden" name="ref" value={transformerRef} />
	<button type="submit" aria-disabled={running ? 'true' : undefined}>
		<Icon name="retry" size={16} />{running
			? 'Running samples...'
			: `Run samples for ${transformerRef}`}
	</button>
	<span class="hint">Runs every sample now; nothing is saved or sent to a vendor.</span>
</form>
{#if running}<p class="visually-hidden" role="status">Running samples</p>{/if}
{#if error}
	<ErrorPanel {error} context="Could not run the samples" headingLevel={3} />
{:else if result}
	<GoldensResult {result} refLabel={transformerRef} />
{/if}

<style>
	.run {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-3);
		margin-bottom: var(--space-3);
	}
	button {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		height: 2.125rem;
		padding: 0 0.9rem;
		border: 1px solid var(--signal);
		border-radius: var(--radius-sm);
		background: var(--signal);
		color: var(--surface);
		font-weight: 700;
		cursor: pointer;
	}
	button[aria-disabled='true'] {
		opacity: 0.6;
		cursor: progress;
	}
	.hint {
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
</style>

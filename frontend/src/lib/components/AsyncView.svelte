<script lang="ts" generics="T">
	import { untrack, type Snippet } from 'svelte';
	import type { ApiResult } from '$lib/api/client';
	import { HubApiError } from '$lib/api/errors';
	import ErrorPanel from './ErrorPanel.svelte';
	import Skeleton from './Skeleton.svelte';

	/**
	 * Shared loading / error / data states for one admin API call (HUB-255). Pages pass the
	 * un-awaited `api.call(...)` from `load`; this renders:
	 * - loading: `loading` snippet or a `Skeleton` labelled "Loading <noun>...";
	 * - error: `ErrorPanel` (code, message, request id + copy, retry hint, Retry);
	 * - data: `children(data)`.
	 * When `result` changes (filters, paging), the previous data stays on screen, marked busy, until
	 * the new answer arrives (admin-console-ux §6: no layout jump while refetching).
	 */
	let {
		result,
		noun,
		onretry,
		loading,
		children,
		headingLevel = 2,
		quiet = false
	}: {
		result: Promise<ApiResult<T>>;
		/** What is loading, for status text: "integration int_evt_brandA". */
		noun: string;
		onretry?: () => void;
		loading?: Snippet;
		children: Snippet<[T]>;
		headingLevel?: 2 | 3;
		/** Background refreshes (auto-refresh): no dimming and no "Updating..." announcement. */
		quiet?: boolean;
	} = $props();

	type View =
		{ status: 'loading' } | { status: 'ok'; data: T } | { status: 'error'; error: HubApiError };

	let view = $state.raw<View>({ status: 'loading' });
	let refreshing = $state(false);

	// Syncs with an external promise; the cleanup drops answers to superseded calls.
	$effect(() => {
		const current = result;
		let live = true;
		const hadData = untrack(() => view.status === 'ok');
		refreshing = hadData;
		if (!hadData) view = { status: 'loading' };
		current.then(
			(r) => {
				if (!live) return;
				view = r.ok ? { status: 'ok', data: r.data } : { status: 'error', error: r.error };
				refreshing = false;
			},
			(e: unknown) => {
				if (!live) return;
				view = {
					status: 'error',
					error: e instanceof HubApiError ? e : HubApiError.network(e)
				};
				refreshing = false;
			}
		);
		return () => {
			live = false;
		};
	});
</script>

{#if view.status === 'loading'}
	{#if loading}{@render loading()}{:else}<Skeleton label="Loading {noun}..." />{/if}
{:else if view.status === 'error'}
	<ErrorPanel error={view.error} context="Could not load {noun}" {onretry} {headingLevel} />
{:else}
	<div class="data" aria-busy={refreshing && !quiet}>
		{@render children(view.data)}
	</div>
	{#if refreshing && !quiet}<p class="refreshing" role="status">Updating {noun}...</p>{/if}
{/if}

<style>
	.data[aria-busy='true'] {
		opacity: 0.6;
		transition: opacity 120ms ease-out;
	}
	.refreshing {
		margin: var(--space-2) 0 0;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
</style>

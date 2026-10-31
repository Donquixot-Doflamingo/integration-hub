<script lang="ts">
	import { untrack } from 'svelte';
	import type { SubmitFunction } from '@sveltejs/kit';
	import { enhance } from '$app/forms';
	import { invalidateAll } from '$app/navigation';
	import {
		fromPlainError,
		isConcurrencyError,
		type ActionFailureData,
		type ActionSuccessData,
		type FieldErrors
	} from '$lib/api/actions';
	import type { ApiResult } from '$lib/api/client';
	import { HubApiError } from '$lib/api/errors';
	import type {
		Integration,
		IntegrationVersion,
		PublishResult,
		ValidationReport,
		VersionDiff
	} from '$lib/api/types';
	import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
	import ErrorPanel from '$lib/components/ErrorPanel.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import Skeleton from '$lib/components/Skeleton.svelte';
	import DiffSplit from '$lib/components/diff/DiffSplit.svelte';
	import { REASON_MAX, REASON_MIN } from '$lib/reason';
	import { summarize } from '$lib/utils/diff';
	import { formatUtc } from '$lib/utils/format';
	import { specToYaml } from '$lib/utils/spec-yaml';
	import ChecksList from './ChecksList.svelte';
	import SpecEditor from './SpecEditor.svelte';
	import {
		reportFromError,
		reportProblems,
		specProblems,
		type SavedDraft,
		type SpecProblem
	} from './draft';

	/**
	 * HUB-259 publish flow: edit the draft spec (create it from the active version if there is
	 * none), save it (the publish checks run right after), review the diff against the active
	 * version, then publish in the confirm dialog with a reason.
	 */
	let {
		integration,
		draft,
		diff,
		onnotice
	}: {
		integration: Integration;
		draft: IntegrationVersion | null;
		diff: Promise<ApiResult<VersionDiff> | null>;
		onnotice: (message: string, changeId: string | null) => void;
	} = $props();

	const id = $derived(integration.integration_id);
	const active = $derived(integration.active_version);

	// The editor keeps its own text across data reloads (saving reloads the page data).
	const initial = untrack(() => specToYaml((draft ?? integration.active)?.spec ?? {}));
	let text = $state(initial);
	let saved = $state(initial);
	let savedVersion = $state<number | null>(null);
	const version = $derived(savedVersion ?? draft?.version ?? null);
	const dirty = $derived(text !== saved);

	let draftReason = $state('');
	let saving = $state(false);
	let checking = $state(false);
	let saveError = $state.raw<HubApiError | null>(null);
	let saveFields = $state.raw<FieldErrors>({});
	let problems = $state.raw<SpecProblem[]>([]);
	let report = $state.raw<ValidationReport | null>(null);
	let publishOpen = $state(false);

	const allProblems = $derived([...problems, ...reportProblems(report)]);
	const blocked = $derived(
		!version
			? 'Save the draft first.'
			: dirty
				? 'Save your changes first, so you publish what you see.'
				: report && !report.valid
					? 'Some publish checks fail.'
					: null
	);
	const conflict = $derived(saveError && isConcurrencyError(saveError) ? saveError : null);

	function failure(data: ActionFailureData | undefined) {
		saveError = data?.error ? fromPlainError(data.error) : null;
		saveFields = data?.fields ?? {};
		problems = specProblems(data?.error);
		report = reportFromError(data?.error) ?? report;
	}

	const save: SubmitFunction = ({ cancel }) => {
		if (saving) return cancel();
		saving = true;
		saveError = null;
		saveFields = {};
		problems = [];
		const sent = text;
		return async ({ result, update }) => {
			try {
				if (result.type === 'success') {
					const d = result.data as ActionSuccessData<SavedDraft>;
					saved = sent;
					savedVersion = d.result.version;
					report = d.result.report;
					draftReason = '';
					onnotice(
						`Saved draft v${d.result.version}. ${d.result.report ? (d.result.report.valid ? 'All publish checks pass.' : 'Some publish checks fail; see below.') : 'The publish checks could not run; run them again.'}`,
						d.changeId
					);
					await update({ reset: false });
				} else if (result.type === 'failure') {
					failure(result.data as ActionFailureData);
				} else {
					saveError = new HubApiError({
						status: result.status ?? 500,
						code: 'UNEXPECTED_RESPONSE',
						message: 'The console could not save the draft. Try again.'
					});
				}
			} finally {
				saving = false;
			}
		};
	};

	const check: SubmitFunction = ({ cancel }) => {
		if (checking || !version) return cancel();
		checking = true;
		saveError = null;
		return async ({ result }) => {
			try {
				if (result.type === 'success') {
					report = (result.data as ActionSuccessData<ValidationReport>).result;
				} else if (result.type === 'failure') {
					failure(result.data as ActionFailureData);
				}
			} finally {
				checking = false;
			}
		};
	};

	function onPublished(d: ActionSuccessData) {
		const r = d.result as PublishResult;
		onnotice(
			`Published v${r.active_version}. It is now the active version${r.previous_active_version ? ` (was v${r.previous_active_version})` : ''}${r.snapshot_version ? `, in snapshot ${r.snapshot_version}` : ''}.`,
			d.changeId
		);
		report = null;
	}

	function onPublishFailed(d: ActionFailureData) {
		problems = specProblems(d.error);
		report = reportFromError(d.error) ?? report;
	}
</script>

<section class="status" aria-label="Draft">
	{#if version}
		<p>
			<strong>Draft v{version}</strong>{#if draft && draft.version === version}, based on {draft.based_on
					? `v${draft.based_on}`
					: 'nothing'}, by {draft.author} on {formatUtc(draft.created_at)}. Reason: {draft.reason}{/if}
		</p>
	{:else}
		<p>
			<strong>No draft yet.</strong> The text below is a copy of {active
				? `the active v${active}`
				: 'nothing (no version exists)'}. Saving creates draft v{(integration.latest_version ?? 0) +
				1}.
		</p>
	{/if}
	{#if dirty}<p class="dirty" role="status"><Icon name="alert" size={14} />Unsaved changes</p>{/if}
</section>

<form class="draft" method="POST" action="?/saveDraft" use:enhance={save} novalidate>
	<SpecEditor
		bind:value={text}
		problems={allProblems}
		error={saveFields.spec ?? null}
		disabled={saving}
	/>
	{#if version}<input type="hidden" name="version" value={version} />{/if}
	{#if !version && active}<input type="hidden" name="based_on" value={active} />{/if}
	<div class="field">
		<label for="draft-reason">
			{version ? 'Why you are changing the draft' : 'Why this version'}
			<span class="muted">(required)</span>
		</label>
		<input
			id="draft-reason"
			name="reason"
			bind:value={draftReason}
			maxlength={REASON_MAX}
			autocomplete="off"
			aria-invalid={saveFields.reason ? 'true' : undefined}
			aria-describedby="draft-reason-hint{saveFields.reason ? ' draft-reason-error' : ''}"
			disabled={saving}
		/>
		<p id="draft-reason-hint" class="hint">
			At least {REASON_MIN} characters. Saved in the change log; the first one becomes the version's reason.
		</p>
		{#if saveFields.reason}
			<p id="draft-reason-error" class="field-error">
				<Icon name="alert" size={14} />{saveFields.reason}
			</p>
		{/if}
	</div>
	{#if conflict}
		<div class="conflict" role="alert">
			<p><strong>Someone else changed {id}.</strong> {conflict.message}</p>
			<p>Reload to see the current draft. Copy your text first if you want to keep it.</p>
			<button type="button" class="secondary" onclick={() => invalidateAll()}>
				<Icon name="retry" size={16} />Reload
			</button>
		</div>
	{:else if saveError}
		<ErrorPanel error={saveError} context="Could not save the draft" headingLevel={3} />
	{/if}
	<div class="buttons">
		<button type="submit" class="secondary" disabled={saving || (!dirty && !!version)}>
			{saving ? 'Saving...' : version ? `Save draft v${version}` : 'Create draft'}
		</button>
	</div>
</form>

{#if version}
	<form method="POST" action="?/validate" use:enhance={check} class="check-form">
		<input type="hidden" name="version" value={version} />
		<button type="submit" class="secondary" disabled={checking || dirty}>
			{checking ? 'Checking...' : `Run publish checks on v${version}`}
		</button>
	</form>
{/if}

{#if report}
	<ChecksList {report} />
{/if}

{#await diff}
	<Skeleton label="Loading the changes against the active version..." />
{:then d}
	{#if d}
		<section class="diff" aria-labelledby="draft-diff-h">
			<h2 id="draft-diff-h">Changes against the active version</h2>
			{#if d.ok}
				<p class="meta" role="status">
					v{d.data.from} to v{d.data.to}: {summarize(d.data.changes)}
				</p>
				<DiffSplit
					unified={d.data.unified}
					oldLabel="v{d.data.from} (active)"
					newLabel="v{d.data.to} (draft)"
				/>
				{#if dirty}<p class="meta">
						This diff is of the saved draft, not of your unsaved text.
					</p>{/if}
			{:else}
				<ErrorPanel
					error={d.error}
					context="Could not load the diff"
					headingLevel={3}
					onretry={invalidateAll}
				/>
			{/if}
		</section>
	{/if}
{/await}

<section class="publish" aria-labelledby="publish-h">
	<h2 id="publish-h">Publish</h2>
	<p class="meta">
		Publishing makes the draft immutable and active. It cannot be undone, only rolled back by
		publishing a copy of an older version.
	</p>
	<button
		type="button"
		class="primary"
		disabled={!!blocked}
		aria-describedby={blocked ? 'publish-blocked' : undefined}
		onclick={() => (publishOpen = true)}>Publish v{version ?? '?'}</button
	>
	{#if blocked}<p id="publish-blocked" class="meta">{blocked}</p>{/if}
</section>

{#if version}
	<ConfirmDialog
		bind:open={publishOpen}
		title="Publish {id} v{version}"
		verb="Publish v{version}"
		busyVerb="Publishing..."
		action="?/publish"
		tone="primary"
		subject={id}
		onsuccess={onPublished}
		onfailure={onPublishFailed}
	>
		<p>
			v{version} becomes the active version{active ? `, replacing v${active}` : ''}. The full
			publish validation runs again and a new config snapshot is built; gateway nodes switch within
			about 10 s.
		</p>
		<p>
			Requests already queued re-resolve to v{version} when they are sent, unless the spec pins them.
			The published version can never be edited.
		</p>
		{#await diff then d}
			{#if d?.ok}<p><strong>Changes:</strong> {summarize(d.data.changes)}</p>{/if}
		{/await}
		{#snippet fields()}
			<input type="hidden" name="version" value={version} />
			<input type="hidden" name="expected_active_version" value={active ?? ''} />
		{/snippet}
		{#snippet extra()}
			{#if report && !report.valid}<ChecksList {report} heading="Failing checks" />{/if}
		{/snippet}
	</ConfirmDialog>
{/if}

<style>
	.status p {
		margin: 0 0 var(--space-2);
		font-size: var(--text-sm);
	}
	.dirty {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		color: var(--status-warn);
		font-weight: 700;
	}
	.draft {
		display: grid;
		gap: var(--space-3);
		max-width: 72rem;
	}
	.field {
		display: grid;
		gap: 0.25rem;
		max-width: 46rem;
	}
	label {
		font-weight: 600;
		font-size: var(--text-sm);
	}
	input:not([type='hidden']) {
		height: 2.125rem;
		padding: 0 0.55rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-size: var(--text-sm);
	}
	[aria-invalid='true'] {
		border-color: var(--status-danger);
	}
	.hint,
	.meta {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.meta {
		font-size: var(--text-sm);
	}
	.field-error {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		margin: 0;
		color: var(--status-danger);
		font-size: var(--text-sm);
		font-weight: 600;
	}
	.conflict {
		padding: var(--space-3);
		border: 1px solid var(--status-warn);
		border-left-width: 4px;
		border-radius: var(--radius-md);
		background: var(--status-warn-wash);
		font-size: var(--text-sm);
		max-width: 46rem;
	}
	.conflict p {
		margin: 0 0 var(--space-2);
	}
	.buttons {
		display: flex;
		gap: var(--space-2);
	}
	.check-form {
		margin: var(--space-3) 0;
	}
	button.primary,
	button.secondary {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		min-height: 2.25rem;
		padding: 0 1rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-weight: 700;
		cursor: pointer;
	}
	button.primary {
		border-color: var(--signal);
		background: var(--signal);
		color: var(--surface);
	}
	button:disabled {
		cursor: not-allowed;
		opacity: 0.55;
	}
	.diff,
	.publish {
		display: grid;
		gap: var(--space-2);
		margin-top: var(--space-5);
		max-width: 72rem;
		justify-items: start;
	}
	.diff {
		justify-items: stretch;
	}
	h2 {
		font-size: var(--text-lg);
	}
</style>

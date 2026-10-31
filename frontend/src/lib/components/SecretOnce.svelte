<script lang="ts">
	import { tick, untrack } from 'svelte';
	import Icon from './Icon.svelte';

	/**
	 * Shows a newly minted secret exactly once (admin-console-ux §3, HUB-271): a modal with the
	 * value in a read-only field, a Copy button and a plain warning that it cannot be shown again.
	 * The page passes the secret straight from the action answer and drops it in `ondone`; this
	 * component keeps no other copy (no store, no storage, no URL, no logging). Escape does not
	 * close it, so the key is not lost by a stray key press: Done needs "I have stored it" ticked.
	 */
	let {
		secret,
		title,
		label,
		notes = [],
		ondone
	}: {
		secret: string;
		/** "Copy the new API key for orders-service". */
		title: string;
		/** Label of the field: "New API key (hk_live_Z9a1...)". */
		label: string;
		/** Extra facts, e.g. how long the replaced key keeps working. */
		notes?: string[];
		/** Close: the page must forget the secret here. */
		ondone: () => void;
	} = $props();

	const id = $props.id();
	let dialog = $state<HTMLElement>();
	let field = $state<HTMLInputElement>();
	let stored = $state(false);
	let copyStatus = $state<'idle' | 'copied' | 'failed'>('idle');

	// Take focus on open (the key field, selected); give it back to what had it on close.
	$effect(() => {
		const before = untrack(() => document.activeElement) as HTMLElement | null;
		tick().then(() => {
			field?.focus();
			field?.select();
		});
		return () => {
			const target = before?.isConnected ? before : document.getElementById('main');
			target?.focus();
		};
	});

	async function copy() {
		try {
			await navigator.clipboard.writeText(secret);
			copyStatus = 'copied';
		} catch {
			copyStatus = 'failed';
			field?.select();
		}
	}

	const focusable =
		'button:not([disabled]), input:not([disabled]):not([type="hidden"]), [href], [tabindex]:not([tabindex="-1"])';

	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') {
			e.preventDefault();
			e.stopPropagation();
			return;
		}
		if (e.key !== 'Tab' || !dialog) return;
		const items = [...dialog.querySelectorAll<HTMLElement>(focusable)];
		const first = items[0];
		const last = items[items.length - 1];
		if (!first || !last) return;
		if (e.shiftKey && document.activeElement === first) {
			e.preventDefault();
			last.focus();
		} else if (!e.shiftKey && document.activeElement === last) {
			e.preventDefault();
			first.focus();
		}
	}
</script>

<div class="backdrop">
	<div
		bind:this={dialog}
		class="dialog"
		role="dialog"
		aria-modal="true"
		aria-labelledby="{id}-title"
		aria-describedby="{id}-warning"
		tabindex="-1"
		{onkeydown}
	>
		<h2 id="{id}-title">{title}</h2>
		<p id="{id}-warning" class="warning">
			<Icon name="alert" size={18} />
			<span
				><strong>This is the only time it is shown.</strong> The Hub keeps only a hash: nobody can show
				it again, not even an admin. Store it in your secret manager now. If it is lost, rotate the key.</span
			>
		</p>
		<div class="field">
			<label for="{id}-secret">{label}</label>
			<div class="row">
				<input
					bind:this={field}
					id="{id}-secret"
					class="secret"
					type="text"
					readonly
					value={secret}
					autocomplete="off"
					spellcheck="false"
					data-1p-ignore
					data-lpignore="true"
					onfocus={(e) => e.currentTarget.select()}
				/>
				<button type="button" class="copy" onclick={copy}>
					<Icon name={copyStatus === 'copied' ? 'check' : 'copy'} size={16} />Copy
				</button>
			</div>
			<p class="status" aria-live="polite">
				{copyStatus === 'copied'
					? 'Copied to the clipboard.'
					: copyStatus === 'failed'
						? 'Could not copy. The key is selected: copy it with your keyboard.'
						: ''}
			</p>
		</div>
		{#each notes as note (note)}<p class="note">{note}</p>{/each}
		<label class="check">
			<input type="checkbox" bind:checked={stored} />
			I have stored it somewhere safe
		</label>
		<div class="buttons">
			<button type="button" class="done" disabled={!stored} onclick={ondone}>Done</button>
		</div>
	</div>
</div>

<style>
	.backdrop {
		position: fixed;
		inset: 0;
		z-index: 60;
		display: grid;
		place-items: start center;
		padding: 8vh var(--space-4) var(--space-4);
		overflow-y: auto;
		background: rgb(10 19 29 / 0.55);
	}
	.dialog {
		width: min(40rem, 100%);
		padding: var(--space-5);
		border: 1px solid var(--rule-strong);
		border-top: 4px solid var(--status-warn);
		border-radius: var(--radius-md);
		background: var(--surface);
		color: var(--ink);
		box-shadow: 0 18px 48px rgb(10 19 29 / 0.35);
		display: grid;
		gap: var(--space-3);
	}
	h2 {
		font-size: var(--text-lg);
	}
	p {
		margin: 0;
	}
	.warning {
		display: flex;
		gap: var(--space-2);
		padding: var(--space-3);
		border: 1px solid var(--status-warn);
		border-left-width: 4px;
		border-radius: var(--radius-sm);
		background: var(--status-warn-wash);
		font-size: var(--text-sm);
	}
	.warning :global(.icon) {
		flex: none;
		color: var(--status-warn);
		margin-top: 0.1rem;
	}
	.field {
		display: grid;
		gap: 0.25rem;
	}
	.field > label {
		font-weight: 600;
		font-size: var(--text-sm);
	}
	.row {
		display: flex;
		gap: var(--space-2);
	}
	.secret {
		flex: 1;
		min-width: 0;
		padding: 0.45rem 0.55rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface-sunken);
		font-family: var(--font-id);
		font-size: var(--text-sm);
	}
	.copy,
	.done {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		min-height: 2.25rem;
		padding: 0 1rem;
		border: 1px solid var(--signal);
		border-radius: var(--radius-sm);
		background: var(--surface);
		color: var(--signal);
		font-weight: 700;
		cursor: pointer;
	}
	.done {
		background: var(--signal);
		color: var(--surface);
	}
	.done:disabled {
		cursor: not-allowed;
		opacity: 0.55;
	}
	.status {
		min-height: 1.2em;
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.note {
		font-size: var(--text-sm);
	}
	.check {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		font-size: var(--text-sm);
		font-weight: 600;
	}
	.buttons {
		display: flex;
		justify-content: flex-end;
	}
</style>

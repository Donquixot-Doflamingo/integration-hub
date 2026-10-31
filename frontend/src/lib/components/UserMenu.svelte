<script lang="ts">
	import type { ApiResult } from '$lib/api/client';
	import type { Me } from '$lib/api/types';
	import { formatUtc } from '$lib/utils/format';

	let { me }: { me: Promise<ApiResult<Me>> } = $props();

	let open = $state(false);
	let trigger = $state<HTMLButtonElement>();
	let root = $state<HTMLDivElement>();

	function initials(u: Me): string {
		const source = u.name || u.email || u.subject;
		const parts = source.split(/[\s@._-]+/).filter(Boolean);
		return ((parts[0]?.[0] ?? '?') + (parts[1]?.[0] ?? '')).toUpperCase();
	}

	function close(returnFocus: boolean) {
		open = false;
		if (returnFocus) trigger?.focus();
	}

	function onkeydown(e: KeyboardEvent) {
		if (open && e.key === 'Escape') {
			e.preventDefault();
			close(true);
		}
	}

	function onpointerdown(e: PointerEvent) {
		if (open && root && !root.contains(e.target as Node)) close(false);
	}
</script>

<svelte:window {onkeydown} {onpointerdown} />

<div class="user" bind:this={root}>
	{#await me}
		<span class="pending" role="status">Loading user...</span>
	{:then res}
		{#if res.ok}
			{@const u = res.data}
			<button
				bind:this={trigger}
				type="button"
				class="trigger"
				aria-expanded={open}
				aria-controls="user-panel"
				onclick={() => (open = !open)}
			>
				<span class="avatar" aria-hidden="true">{initials(u)}</span>
				<span class="who">
					<span class="name">{u.name || u.email || u.subject}</span>
					<span class="role">{u.roles.join(', ') || 'no role'}</span>
				</span>
				<span class="visually-hidden">Account details</span>
			</button>
			<div id="user-panel" class="panel" hidden={!open}>
				<dl>
					{#if u.email}<div>
							<dt>Email</dt>
							<dd>{u.email}</dd>
						</div>{/if}
					<div>
						<dt>Roles</dt>
						<dd>{u.roles.join(', ') || 'None'}</dd>
					</div>
					<div>
						<dt>Tenants</dt>
						<dd>{u.tenants.length ? u.tenants.join(', ') : 'All tenants'}</dd>
					</div>
					{#if u.token_expires_at}
						<div>
							<dt>Session ends</dt>
							<dd>{formatUtc(u.token_expires_at)}</dd>
						</div>
					{/if}
				</dl>
				<p class="note">Sign-in and sign-out arrive with OIDC login (HUB-253).</p>
			</div>
		{:else}
			<span class="unavailable" title={`${res.error.code} ${res.error.requestId ?? ''}`}>
				User unavailable ({res.error.code})
			</span>
		{/if}
	{/await}
</div>

<style>
	.user {
		position: relative;
	}
	.trigger {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		padding: 0.2rem 0.5rem 0.2rem 0.25rem;
		border: 1px solid transparent;
		border-radius: var(--radius-md);
		background: none;
		cursor: pointer;
		text-align: left;
	}
	.trigger:hover,
	.trigger[aria-expanded='true'] {
		border-color: var(--rule);
		background: var(--surface);
	}
	.avatar {
		display: grid;
		place-items: center;
		width: 2rem;
		height: 2rem;
		border-radius: 50%;
		background: var(--rail);
		color: var(--signal-on-rail);
		font-weight: 700;
		font-size: var(--text-xs);
	}
	.who {
		display: grid;
		line-height: 1.15;
	}
	.name {
		font-size: var(--text-sm);
		font-weight: 600;
	}
	.role {
		font-size: var(--text-xs);
		color: var(--ink-muted);
	}
	.panel {
		position: absolute;
		right: 0;
		top: calc(100% + 0.35rem);
		z-index: 20;
		width: 19rem;
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--rule);
		border-radius: var(--radius-md);
		background: var(--surface);
		box-shadow: 0 8px 24px rgb(10 20 35 / 0.18);
	}
	.panel[hidden] {
		display: none;
	}
	dl {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		font-size: var(--text-sm);
	}
	dl div {
		display: grid;
		grid-template-columns: 6rem 1fr;
	}
	dt {
		color: var(--ink-muted);
	}
	dd {
		margin: 0;
		overflow-wrap: anywhere;
	}
	.note,
	.pending,
	.unavailable {
		font-size: var(--text-xs);
		color: var(--ink-muted);
	}
	.note {
		margin: var(--space-3) 0 0;
		padding-top: var(--space-2);
		border-top: 1px solid var(--rule);
	}
</style>

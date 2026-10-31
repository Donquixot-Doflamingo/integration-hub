<script lang="ts">
	import type { ApiResult } from '$lib/api/client';
	import type { Me, SessionUser } from '$lib/api/types';
	import { highestRole, roleLabels } from '$lib/auth/roles';
	import { formatUtc } from '$lib/utils/format';

	let {
		me,
		user,
		provider,
		expiresAt
	}: {
		/** `/me` answer: the API's view of roles and tenants (drives gating). */
		me: ApiResult<Me>;
		/** Session identity, shown when `/me` failed. */
		user: SessionUser;
		provider: 'dev' | 'oidc';
		expiresAt: string;
	} = $props();

	let open = $state(false);
	let trigger = $state<HTMLButtonElement>();
	let root = $state<HTMLDivElement>();

	const who = $derived(me.ok ? me.data : user);
	const displayName = $derived(who.name || who.email || who.subject);
	const role = $derived(highestRole(who.roles));

	function initials(source: string): string {
		const parts = source.split(/[\s@._|-]+/).filter(Boolean);
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
	<button
		bind:this={trigger}
		type="button"
		class="trigger"
		aria-expanded={open}
		aria-controls="user-panel"
		onclick={() => (open = !open)}
	>
		<span class="avatar" aria-hidden="true">{initials(displayName)}</span>
		<span class="who">
			<span class="name">{displayName}</span>
			<span class="role">{role ? roleLabels[role] : 'No role'}</span>
		</span>
		<span class="visually-hidden">Account details</span>
	</button>
	<div id="user-panel" class="panel" hidden={!open}>
		<dl>
			{#if who.email}<div>
					<dt>Email</dt>
					<dd>{who.email}</dd>
				</div>{/if}
			<div>
				<dt>Roles</dt>
				<dd>{who.roles.map((r) => roleLabels[r]).join(', ') || 'None'}</dd>
			</div>
			<div>
				<dt>Tenants</dt>
				<dd>{who.tenants.length ? who.tenants.join(', ') : 'All tenants'}</dd>
			</div>
			<div>
				<dt>Sign-in</dt>
				<dd>{provider === 'dev' ? 'Development role picker' : 'Single sign-on'}</dd>
			</div>
			<div>
				<dt>Session ends</dt>
				<dd>
					{formatUtc(me.ok && me.data.token_expires_at ? me.data.token_expires_at : expiresAt)}
				</dd>
			</div>
		</dl>
		{#if !me.ok}
			<p class="note" role="status">
				Could not load your roles from the admin API ({me.error.code}). Changes are hidden until it
				answers.
			</p>
		{/if}
		<form method="POST" action="/logout" class="signout">
			<button type="submit">Sign out</button>
		</form>
	</div>
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
	.note {
		margin: var(--space-3) 0 0;
		font-size: var(--text-xs);
		color: var(--ink-muted);
	}
	.signout {
		margin: var(--space-3) 0 0;
		padding-top: var(--space-3);
		border-top: 1px solid var(--rule);
	}
	.signout button {
		padding: 0.3rem 0.8rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
		font-weight: 600;
		font-size: var(--text-sm);
		cursor: pointer;
	}
	.signout button:hover {
		border-color: var(--ink-muted);
	}
</style>

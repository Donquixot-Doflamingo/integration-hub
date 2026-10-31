<script lang="ts">
	import { appName, pageTitle } from '$lib/app-info';
	import { ROLES, roleLabels, roleSummaries } from '$lib/auth/roles';
	import Icon from '$lib/components/Icon.svelte';
	import type { ActionData, PageData } from './$types';

	let { data, form }: { data: PageData; form: ActionData } = $props();

	const values = $derived(form?.values);
	const errorFor = (field: string) => (form?.field === field ? form.message : null);
	const formError = $derived(form && !form.field ? form.message : null);
	// The tenant field only matters for tenant_viewer. After a failed post, keep the posted role.
	let role = $state<string>('editor');
	$effect.pre(() => {
		if (values?.role) role = values.role;
	});
</script>

<svelte:head>
	<title>{pageTitle('Sign in')}</title>
</svelte:head>

<main class="login">
	<div class="panel">
		<p class="brand">
			<svg viewBox="0 0 32 32" width="28" height="28" aria-hidden="true">
				<rect width="32" height="32" rx="6" fill="currentColor" opacity="0.14" />
				<path
					d="M8 9v14M24 9v14M8 16h16"
					stroke="currentColor"
					stroke-width="3"
					stroke-linecap="round"
				/>
			</svg>
			{appName}
		</p>
		<h1>Sign in</h1>

		{#if data.expired}
			<p class="notice warn" role="status">
				<Icon name="alert" size={16} /> Your session is no longer accepted by the admin API. Sign in again.
			</p>
		{/if}
		{#if data.signedInAs}
			<p class="notice" role="status">
				Signed in as {data.signedInAs}. Signing in again replaces that session.
			</p>
		{/if}
		{#if data.login.notice}<p class="lede">{data.login.notice}</p>{/if}

		{#if formError}
			<p class="notice danger" role="alert"><Icon name="alert" size={16} /> {formError}</p>
		{/if}

		{#if data.login.provider === 'dev' && data.login.ready}
			<form method="POST" novalidate>
				<input type="hidden" name="next" value={data.next} />
				<fieldset aria-describedby={errorFor('role') ? 'role-error' : undefined}>
					<legend>Role</legend>
					{#each ROLES as r (r)}
						<label class="role">
							<input type="radio" name="role" value={r} bind:group={role} required />
							<span>
								<span class="role-name">{roleLabels[r]}</span>
								<span class="role-summary">{roleSummaries[r]}</span>
							</span>
						</label>
					{/each}
					{#if errorFor('role')}<p id="role-error" class="field-error">{errorFor('role')}</p>{/if}
				</fieldset>

				<div class="field" hidden={role !== 'tenant_viewer'}>
					<label for="tenants">Tenants <span class="hint">(tenant viewer only)</span></label>
					<input
						id="tenants"
						name="tenants"
						value={values?.tenants ?? 'orders-service'}
						autocomplete="off"
						aria-describedby="tenants-hint{errorFor('tenants') ? ' tenants-error' : ''}"
						aria-invalid={errorFor('tenants') ? 'true' : undefined}
					/>
					<p id="tenants-hint" class="hint">Tenant ids, separated by commas.</p>
					{#if errorFor('tenants')}
						<p id="tenants-error" class="field-error">{errorFor('tenants')}</p>
					{/if}
				</div>

				<div class="field">
					<label for="name">Name</label>
					<input id="name" name="name" value={values?.name ?? 'Priya Sharma'} autocomplete="name" />
				</div>
				<div class="field">
					<label for="email">Email <span class="hint">(optional)</span></label>
					<input
						id="email"
						name="email"
						type="email"
						value={values?.email ?? 'priya@example.com'}
						autocomplete="email"
						aria-describedby={errorFor('email') ? 'email-error' : undefined}
						aria-invalid={errorFor('email') ? 'true' : undefined}
					/>
					{#if errorFor('email')}<p id="email-error" class="field-error">
							{errorFor('email')}
						</p>{/if}
				</div>

				<button type="submit" class="primary">Sign in</button>
			</form>
		{:else}
			<div class="unavailable">
				<p>Sign-in is unavailable on this server.</p>
				{#if data.login.missing.length}
					<p class="hint">Missing settings:</p>
					<ul>
						{#each data.login.missing as key (key)}<li><code>{key}</code></li>{/each}
					</ul>
				{/if}
				<p class="hint">
					For local work run <code>npm run dev:mock</code>, or set
					<code>PUBLIC_AUTH_MODE=dev</code>.
				</p>
			</div>
		{/if}
	</div>
</main>

<style>
	.login {
		display: grid;
		place-items: start center;
		min-height: 100vh;
		padding: 12vh var(--space-4) var(--space-6);
		background: linear-gradient(var(--rail) 0 var(--band-height), transparent 0);
	}
	.panel {
		width: min(100%, 30rem);
		padding: var(--space-5) var(--space-5) var(--space-6);
		border: 1px solid var(--rule);
		border-top: 4px solid var(--signal);
		border-radius: var(--radius-md);
		background: var(--surface);
	}
	.brand {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		margin: 0 0 var(--space-4);
		color: var(--signal);
		font-weight: 700;
		font-size: var(--text-sm);
	}
	.brand svg {
		color: var(--signal);
	}
	h1 {
		font-size: var(--text-xl);
	}
	.lede {
		margin: var(--space-2) 0 0;
		color: var(--ink-muted);
		font-size: var(--text-sm);
	}
	.notice {
		display: flex;
		gap: var(--space-2);
		align-items: flex-start;
		margin: var(--space-3) 0 0;
		padding: var(--space-2) var(--space-3);
		border-radius: var(--radius-sm);
		background: var(--surface-sunken);
		font-size: var(--text-sm);
	}
	.notice.warn {
		background: var(--status-warn-wash);
		color: var(--ink);
	}
	.notice.warn :global(.icon) {
		color: var(--status-warn);
	}
	.notice.danger {
		background: var(--status-danger-wash);
	}
	.notice.danger :global(.icon) {
		color: var(--status-danger);
	}
	form {
		display: grid;
		gap: var(--space-4);
		margin-top: var(--space-5);
	}
	fieldset {
		display: grid;
		gap: var(--space-1);
		margin: 0;
		padding: 0;
		border: 0;
	}
	legend,
	label {
		font-weight: 600;
		font-size: var(--text-sm);
	}
	legend {
		margin-bottom: var(--space-1);
	}
	.role {
		display: flex;
		gap: var(--space-3);
		align-items: flex-start;
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--rule);
		border-radius: var(--radius-sm);
		cursor: pointer;
		font-weight: 400;
	}
	.role:has(input:checked) {
		border-color: var(--signal);
		background: var(--signal-wash);
	}
	.role input {
		margin-top: 0.2rem;
		accent-color: var(--signal);
	}
	.role > span {
		display: grid;
	}
	.role-name {
		font-weight: 700;
	}
	.role-summary {
		color: var(--ink-muted);
		font-size: var(--text-xs);
	}
	.field {
		display: grid;
		gap: var(--space-1);
	}
	.field input {
		padding: 0.4rem 0.6rem;
		border: 1px solid var(--rule-strong);
		border-radius: var(--radius-sm);
		background: var(--surface);
	}
	.field input[aria-invalid='true'] {
		border-color: var(--status-danger);
	}
	.field[hidden] {
		display: none;
	}
	.hint {
		margin: 0;
		color: var(--ink-muted);
		font-size: var(--text-xs);
		font-weight: 400;
	}
	.field-error {
		margin: 0;
		color: var(--status-danger);
		font-size: var(--text-sm);
	}
	.primary {
		justify-self: start;
		padding: 0.5rem 1.25rem;
		border: 1px solid var(--signal);
		border-radius: var(--radius-sm);
		background: var(--signal);
		color: var(--surface);
		font-weight: 700;
		cursor: pointer;
	}
	.primary:hover {
		filter: brightness(1.08);
	}
	.unavailable {
		margin-top: var(--space-4);
		font-size: var(--text-sm);
	}
	.unavailable ul {
		margin: var(--space-1) 0 var(--space-3);
		padding-left: var(--space-5);
	}
</style>

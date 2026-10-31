<script lang="ts">
	import { resolve } from '$app/paths';
	import { sectionFor, sections } from '$lib/nav';
	import Icon from './Icon.svelte';

	let { pathname, id = 'primary-nav' }: { pathname: string; id?: string } = $props();

	const active = $derived(sectionFor(pathname)?.href);
</script>

<nav {id} class="sidenav" aria-label="Sections">
	<ul>
		{#each sections as s (s.href)}
			<li>
				<a href={resolve(s.href)} aria-current={active === s.href ? 'page' : undefined}>
					<Icon name={s.icon} />
					<span>{s.label}</span>
				</a>
			</li>
		{/each}
	</ul>
</nav>

<style>
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 2px;
	}
	a {
		position: relative;
		display: flex;
		align-items: center;
		gap: var(--space-3);
		min-height: 2.5rem;
		padding: 0 var(--space-3) 0 var(--space-4);
		color: var(--rail-ink-muted);
		text-decoration: none;
		font-weight: 500;
		border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
	}
	a:hover {
		color: var(--rail-ink);
		background: var(--rail-active);
	}
	a:focus-visible {
		box-shadow: inset 0 0 0 2px var(--signal-on-rail);
	}
	/* Active route: a lit "signal lamp" bar plus bold text, not colour alone. */
	a[aria-current='page'] {
		color: #ffffff;
		font-weight: 700;
		background: var(--rail-active);
	}
	a[aria-current='page']::before {
		content: '';
		position: absolute;
		left: 0;
		top: 0.4rem;
		bottom: 0.4rem;
		width: 4px;
		border-radius: 0 2px 2px 0;
		background: var(--signal-on-rail);
	}
	a[aria-current='page'] :global(.icon) {
		color: var(--signal-on-rail);
	}
</style>

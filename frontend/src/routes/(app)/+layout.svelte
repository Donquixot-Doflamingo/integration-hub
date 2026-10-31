<script lang="ts">
	import type { Snippet } from 'svelte';
	import { afterNavigate } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { adminApiBase } from '$lib/api/console';
	import Breadcrumbs from '$lib/components/Breadcrumbs.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import SideNav from '$lib/components/SideNav.svelte';
	import ThemeSwitch from '$lib/components/ThemeSwitch.svelte';
	import UserMenu from '$lib/components/UserMenu.svelte';
	import type { LayoutData } from './$types';

	let { data, children }: { data: LayoutData; children: Snippet } = $props();

	// Below 1024 px the rail becomes a drawer opened from the header.
	let navOpen = $state(false);
	let menuButton = $state<HTMLButtonElement>();

	afterNavigate(() => (navOpen = false));

	function onkeydown(e: KeyboardEvent) {
		if (navOpen && e.key === 'Escape') {
			navOpen = false;
			menuButton?.focus();
		}
	}
</script>

<svelte:window {onkeydown} />

<a class="skip" href="#main">Skip to content</a>

<div class="shell" class:nav-open={navOpen}>
	<div class="band" class:mock={__API_MOCKING__}>
		{#if __API_MOCKING__}
			<strong>Mock mode.</strong> Every answer comes from the admin contract examples; no backend is connected.
		{:else}
			Admin API <code>{adminApiBase()}</code>
		{/if}
	</div>

	<aside class="rail">
		<a class="brand" href={resolve('/')}>
			<svg viewBox="0 0 32 32" width="28" height="28" aria-hidden="true">
				<rect width="32" height="32" rx="6" fill="currentColor" opacity="0.14" />
				<path
					d="M8 9v14M24 9v14M8 16h16"
					stroke="currentColor"
					stroke-width="3"
					stroke-linecap="round"
				/>
			</svg>
			<span>
				<span class="brand-name">Integration Hub</span>
				<span class="brand-sub">Control plane</span>
			</span>
		</a>
		<SideNav pathname={page.url.pathname} />
	</aside>

	<button
		class="scrim"
		type="button"
		tabindex="-1"
		aria-hidden="true"
		onclick={() => (navOpen = false)}
	></button>

	<header class="topbar">
		<button
			bind:this={menuButton}
			class="menu"
			type="button"
			aria-expanded={navOpen}
			aria-controls="primary-nav"
			onclick={() => (navOpen = !navOpen)}
		>
			<Icon name={navOpen ? 'close' : 'menu'} />
			<span>Sections</span>
		</button>
		<Breadcrumbs pathname={page.url.pathname} />
		<div class="tools">
			<ThemeSwitch />
			<UserMenu
				me={data.me}
				user={data.session.user}
				provider={data.session.provider}
				expiresAt={data.session.expiresAt}
			/>
		</div>
	</header>

	<main id="main" tabindex="-1">
		{@render children()}
	</main>
</div>

<style>
	.skip {
		position: absolute;
		left: var(--space-2);
		top: -3rem;
		z-index: 100;
		padding: var(--space-2) var(--space-3);
		background: var(--surface);
		color: var(--ink);
		border: 2px solid var(--signal);
		border-radius: var(--radius-sm);
	}
	.skip:focus {
		top: var(--space-2);
	}

	.shell {
		display: grid;
		grid-template-columns: var(--rail-width) minmax(0, 1fr);
		grid-template-rows: var(--band-height) var(--header-height) 1fr;
		grid-template-areas:
			'band band'
			'rail top'
			'rail main';
		min-height: 100vh;
	}

	/* The one loud element: which control plane this console is talking to. */
	.band {
		grid-area: band;
		display: flex;
		align-items: center;
		gap: var(--space-1);
		padding: 0 var(--space-4);
		font-size: var(--text-xs);
		color: var(--rail-ink);
		background: var(--rail);
		border-bottom: 1px solid rgb(255 255 255 / 0.08);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.band.mock {
		color: #10231f;
		background: repeating-linear-gradient(-45deg, #7fd8cc 0 12px, #9fe3da 12px 24px);
	}
	.band code {
		color: var(--signal-on-rail);
	}

	.rail {
		grid-area: rail;
		position: sticky;
		top: 0;
		align-self: start;
		height: calc(100vh - var(--band-height));
		overflow-y: auto;
		background: var(--rail);
		color: var(--rail-ink);
		padding-bottom: var(--space-4);
	}
	.brand {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		height: var(--header-height);
		padding: 0 var(--space-4);
		margin-bottom: var(--space-2);
		color: var(--signal-on-rail);
		text-decoration: none;
		border-bottom: 1px solid rgb(255 255 255 / 0.08);
	}
	.brand > span {
		display: grid;
		line-height: 1.1;
	}
	.brand-name {
		color: #ffffff;
		font-weight: 700;
	}
	.brand-sub {
		color: var(--rail-ink-muted);
		font-size: var(--text-xs);
	}
	.brand:focus-visible {
		box-shadow: inset 0 0 0 2px var(--signal-on-rail);
	}

	.topbar {
		grid-area: top;
		position: sticky;
		top: 0;
		z-index: 10;
		display: flex;
		align-items: center;
		gap: var(--space-4);
		padding: 0 var(--space-5);
		background: var(--paper);
		border-bottom: 1px solid var(--rule);
	}
	.topbar :global(.crumbs) {
		flex: 1;
		min-width: 0;
	}
	.tools {
		display: flex;
		align-items: center;
		gap: var(--space-3);
	}
	.menu {
		display: none;
		align-items: center;
		gap: var(--space-2);
		padding: 0.3rem 0.6rem;
		border: 1px solid var(--rule);
		border-radius: var(--radius-sm);
		background: var(--surface);
		cursor: pointer;
		font-weight: 600;
		font-size: var(--text-sm);
	}

	main {
		grid-area: main;
		padding: var(--space-5) var(--space-5) var(--space-6);
		min-width: 0;
	}
	main:focus {
		outline: none;
	}

	.scrim {
		display: none;
	}

	/* Tablet (768 px and up to 1023 px): rail becomes a drawer. */
	@media (max-width: 1023px) {
		.shell {
			grid-template-columns: minmax(0, 1fr);
			grid-template-areas:
				'band'
				'top'
				'main';
		}
		.menu {
			display: inline-flex;
		}
		/* The drawer opens below the header so the Sections toggle stays visible to close it. */
		.rail {
			position: fixed;
			top: calc(var(--band-height) + var(--header-height));
			left: 0;
			bottom: 0;
			z-index: 30;
			width: var(--rail-width);
			height: auto;
			transform: translateX(-100%);
			visibility: hidden;
			transition:
				transform 160ms ease-out,
				visibility 0s linear 160ms;
		}
		.nav-open .rail {
			transform: none;
			visibility: visible;
			box-shadow: 6px 0 24px rgb(5 12 22 / 0.35);
			transition: transform 160ms ease-out;
		}
		.nav-open .scrim {
			display: block;
			position: fixed;
			inset: calc(var(--band-height) + var(--header-height)) 0 0 0;
			z-index: 25;
			border: 0;
			background: rgb(10 20 35 / 0.4);
		}
		.topbar {
			padding: 0 var(--space-4);
			gap: var(--space-3);
		}
		main {
			padding: var(--space-4);
		}
	}

	@media (max-width: 860px) {
		.tools :global(.who) {
			display: none;
		}
	}
</style>

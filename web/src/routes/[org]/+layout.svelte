<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { session } from '../../lib/session.svelte';
	import { theme } from '../../lib/theme.svelte';
	import { visibleNav } from '../../lib/modules/registry';
	import { core } from '../../lib/modules/core/manifest';
	import { property } from '../../lib/modules/property/manifest';
	import { timesheets } from '../../lib/modules/timesheets/manifest';
	import { reconciliation } from '../../lib/modules/reconciliation/manifest';
	import { api } from '../../lib/api';
	import Drawer from '../../lib/components/Drawer.svelte';

	let { children } = $props();

	const manifests = [core, property, timesheets, reconciliation];

	$effect(() => {
		session.setOrg(page.params.org ?? '');
	});

	let drawerOpen = $state(false);

	// Closing on every navigation (not just an explicit link click) also
	// covers browser back/forward and the org switcher, which navigates
	// without going through a nav link at all.
	$effect(() => {
		page.url.pathname;
		drawerOpen = false;
	});

	const themeLabel = $derived(
		theme.current === 'system' ? 'Auto' : theme.current === 'light' ? 'Light' : 'Dark'
	);

	const nav = $derived(
		session.currentOrg ? visibleNav(manifests, session.modules, session.currentOrg.perms) : []
	);

	async function logout() {
		try {
			await api('POST', '/api/v1/auth/logout');
		} catch {
			// The session cookie may already be gone (expired, or cleared by
			// a previous attempt); either way the user wants out, so clear
			// client state and leave regardless of whether the server call
			// itself succeeded.
		} finally {
			session.clear();
			goto('/login');
		}
	}

	function switchOrg(slug: string) {
		goto(`/${slug}`);
	}
</script>

{#snippet navList()}
	{#each nav as group (group.module.name)}
		{#if group.module.name === 'core' || group.items.length > 0}
			<div class="nav-group">
				<p class="nav-group-label">{group.module.label}</p>
				<ul>
					{#each group.items as item (item.href)}
						<li>
							<a href={`/${page.params.org}/${item.href}`} onclick={() => (drawerOpen = false)}
							>{item.label}</a
						>
						</li>
					{/each}
				</ul>
			</div>
		{/if}
	{/each}
{/snippet}

<a href="#main-content" class="skip-link">Skip to content</a>

<div class="shell">
	<header class="topbar">
		<button
			type="button"
			class="menu-button"
			aria-label="Open navigation"
			onclick={() => (drawerOpen = true)}
		>
			☰
		</button>

		<div class="org-switcher">
			<label for="org-select">Organisation</label>
			<select
				id="org-select"
				value={page.params.org}
				onchange={(e) => switchOrg((e.target as HTMLSelectElement).value)}
			>
				{#each session.me?.orgs ?? [] as org (org.slug)}
					<option value={org.slug}>{org.name}</option>
				{/each}
			</select>
		</div>

		<div class="topbar-actions">
			<button
				type="button"
				class="icon-button"
				onclick={() => theme.cycle()}
				aria-label={`Theme: ${themeLabel} (click to change)`}
			>
				{themeLabel}
			</button>
			<div class="user-menu">
				<span>{session.me?.user.name}</span>
				<button type="button" onclick={logout}>Logout</button>
			</div>
		</div>
	</header>

	<nav class="sidebar" aria-label="Primary">
		{@render navList()}
	</nav>

	<main id="main-content">
		<!-- Switching org keeps the same route, so SvelteKit would reuse the
		     page component and its onMount-loaded data would still be the
		     previous org's. Keying on the org remounts the page instead. -->
		{#key page.params.org}
			{@render children()}
		{/key}
	</main>
</div>

<Drawer bind:open={drawerOpen} label="Navigation">
	<nav aria-label="Main">
		{@render navList()}
	</nav>
</Drawer>

<style>
	.shell {
		display: grid;
		grid-template-columns: 16rem 1fr;
		grid-template-rows: auto 1fr;
		grid-template-areas: 'topbar topbar' 'sidebar main';
		min-height: 100vh;
	}

	.topbar {
		grid-area: topbar;
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2) var(--space-4);
		padding: var(--space-3) var(--space-4);
		background: var(--surface);
		border-bottom: 1px solid var(--border);
	}

	.menu-button {
		display: none;
		background: none;
		border: 1px solid var(--border);
		border-radius: var(--radius-md);
		padding: var(--space-2) var(--space-3);
		cursor: pointer;
		flex-shrink: 0;
	}

	.org-switcher {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		min-width: 0;
	}

	.org-switcher label {
		font-size: var(--text-sm);
		color: var(--text-muted);
		white-space: nowrap;
	}

	.org-switcher select {
		min-width: 0;
		max-width: 10rem;
	}

	.topbar-actions {
		margin-left: auto;
		display: flex;
		align-items: center;
		gap: var(--space-2);
		flex-wrap: wrap;
	}

	.icon-button,
	.user-menu button {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius-md);
		padding: var(--space-2) var(--space-3);
		cursor: pointer;
	}

	.user-menu {
		display: flex;
		align-items: center;
		gap: var(--space-2);
	}

	.user-menu span {
		max-width: 8rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.sidebar {
		grid-area: sidebar;
		padding: var(--space-4);
		border-right: 1px solid var(--border);
		background: var(--surface);
	}

	.nav-group {
		margin-bottom: var(--space-6);
	}

	.nav-group-label {
		font-size: var(--text-sm);
		font-weight: 600;
		color: var(--text-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		margin: 0 0 var(--space-2);
	}

	.nav-group ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-1);
	}

	.nav-group a {
		display: block;
		padding: var(--space-2) var(--space-3);
		border-radius: var(--radius-md);
		color: var(--text);
		text-decoration: none;
	}

	.nav-group a:hover {
		background: var(--surface-raised);
	}

	main {
		grid-area: main;
		padding: var(--space-6);
		min-width: 0;
	}

	@media (max-width: 767px) {
		.shell {
			grid-template-columns: 1fr;
			grid-template-areas: 'topbar' 'main';
		}
		.sidebar {
			display: none;
		}
		.menu-button {
			display: inline-flex;
		}
	}

	@media (max-width: 374px) {
		.user-menu span {
			display: none;
		}
	}
</style>

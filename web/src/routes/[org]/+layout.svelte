<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { session } from '../../lib/session.svelte';
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

	type Theme = 'system' | 'light' | 'dark';
	const THEME_KEY = 'shigoto-theme';

	function readStoredTheme(): Theme {
		try {
			const saved = localStorage.getItem(THEME_KEY);
			if (saved === 'light' || saved === 'dark') return saved;
		} catch {
			// Private browsing or blocked storage: fall back to system.
		}
		return 'system';
	}

	let theme = $state<Theme>('system');

	$effect(() => {
		theme = readStoredTheme();
	});

	$effect(() => {
		if (theme === 'system') {
			document.documentElement.removeAttribute('data-theme');
		} else {
			document.documentElement.setAttribute('data-theme', theme);
		}
		try {
			if (theme === 'system') {
				localStorage.removeItem(THEME_KEY);
			} else {
				localStorage.setItem(THEME_KEY, theme);
			}
		} catch {
			// Private browsing or blocked storage: the toggle still works
			// for this page load, it just won't persist.
		}
	});

	function cycleTheme() {
		theme = theme === 'system' ? 'light' : theme === 'light' ? 'dark' : 'system';
	}

	const nav = $derived(
		session.currentOrg ? visibleNav(manifests, session.modules, session.currentOrg.perms) : []
	);

	async function logout() {
		await api('POST', '/api/v1/auth/logout');
		goto('/login');
	}

	function switchOrg(slug: string) {
		drawerOpen = false;
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
							<a href={`/${page.params.org}/${item.href}`}>{item.label}</a>
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
				onclick={cycleTheme}
				aria-label={`Theme: ${theme} (click to change)`}
			>
				{theme === 'system' ? 'Auto' : theme === 'light' ? 'Light' : 'Dark'}
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
		{@render children()}
	</main>
</div>

<Drawer bind:open={drawerOpen} label="Navigation">
	{@render navList()}
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
		align-items: center;
		gap: var(--space-4);
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
	}

	.org-switcher {
		display: flex;
		align-items: center;
		gap: var(--space-2);
	}

	.org-switcher label {
		font-size: var(--text-sm);
		color: var(--text-muted);
	}

	.topbar-actions {
		margin-left: auto;
		display: flex;
		align-items: center;
		gap: var(--space-3);
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

	@media (max-width: 768px) {
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
</style>

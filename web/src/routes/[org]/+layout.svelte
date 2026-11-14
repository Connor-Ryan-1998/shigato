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
	const activeHref = $derived(nav.flatMap((group) => group.items).map((item) => `/${page.params.org}/${item.href}`.replace(/\/$/, '')).filter((href) => page.url.pathname === href || page.url.pathname.startsWith(`${href}/`)).sort((a, b) => b.length - a.length)[0]);

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
	{#each nav as group (group.id)}
		<ul class="menu w-full">
			<li>
				<details open>
					<summary class="font-semibold">{group.label}</summary>
					<ul>
						{#each group.items as item (item.href)}
							<li>
								<a href={`/${page.params.org}/${item.href}`} aria-current={activeHref === `/${page.params.org}/${item.href}`.replace(/\/$/, '') ? 'page' : undefined} class:menu-active={activeHref === `/${page.params.org}/${item.href}`.replace(/\/$/, '')} onclick={() => (drawerOpen = false)}
									>{item.label}</a
								>
							</li>
						{/each}
					</ul>
				</details>
			</li>
		</ul>
	{/each}
{/snippet}

<a
	href="#main-content"
	class="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50 focus:rounded-box focus:bg-base-100 focus:px-4 focus:py-2 focus:shadow"
>Skip to content</a>

<div class="grid min-h-screen grid-cols-1 grid-rows-[auto_1fr] bg-base-200 md:grid-cols-[16rem_1fr] print:block print:bg-white">
	<header class="navbar col-span-full flex-wrap gap-2 border-b border-base-300 bg-base-100 px-4 print:hidden">
		<button
			type="button"
			class="btn btn-square btn-ghost md:hidden"
			aria-label="Open navigation"
			onclick={() => (drawerOpen = true)}
		>
			☰
		</button>

		<div class="flex min-w-0 items-center gap-2">
			<label for="org-select" class="text-sm whitespace-nowrap text-base-content/70">Organisation</label>
			<select
				class="select select-sm max-w-40 min-w-0"
				id="org-select"
				value={page.params.org}
				onchange={(e) => switchOrg((e.target as HTMLSelectElement).value)}
			>
				{#each session.me?.orgs ?? [] as org (org.slug)}
					<option value={org.slug}>{org.name}</option>
				{/each}
			</select>
		</div>

		<div class="ml-auto flex flex-wrap items-center gap-2">
			<button
				type="button"
				class="btn btn-ghost btn-sm"
				onclick={() => theme.cycle()}
				aria-label={`Theme: ${themeLabel} (click to change)`}
			>
				{themeLabel}
			</button>
			<div class="flex items-center gap-2">
				<span class="hidden max-w-32 truncate min-[375px]:inline">{session.me?.user.name}</span>
				{#if session.me?.orgs.some((o) => o.portal)}
					<a href="/portal" class="btn btn-ghost btn-sm">Portal</a>
				{/if}
				<button type="button" class="btn btn-outline btn-sm" onclick={logout}>Logout</button>
			</div>
		</div>
	</header>

	<nav class="hidden border-r border-base-300 bg-base-100 py-2 md:block print:hidden" aria-label="Primary">
		{@render navList()}
	</nav>

	<main id="main-content" class="min-w-0 p-4 md:p-6">
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

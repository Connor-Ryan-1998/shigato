<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { session } from '../../lib/session.svelte';
	import { theme } from '../../lib/theme.svelte';
	import { api } from '../../lib/api';

	// The portal's own minimal shell - no staff nav, no org switcher, no
	// module registry: it only ever shows a tenant/client their own org's
	// invoices and (tenants only) leases. Kept entirely separate from
	// [org]/+layout.svelte so no staff navigation or data can ever render
	// here (global-constraints.md "Portal isolation").
	let { children } = $props();

	const org = $derived(page.params.org ?? '');
	const currentOrg = $derived(session.me?.orgs.find((o) => o.slug === org) ?? null);

	const themeLabel = $derived(
		theme.current === 'system' ? 'Auto' : theme.current === 'light' ? 'Light' : 'Dark'
	);

	async function logout() {
		try {
			await api('POST', '/api/v1/auth/logout');
		} catch {
			// The session cookie may already be gone; the user wants out
			// regardless of whether the server call itself succeeded.
		} finally {
			session.clear();
			goto('/login');
		}
	}
</script>

<div class="portal-shell">
	<header class="topbar">
		<span class="org-name">{currentOrg?.name ?? 'Portal'}</span>
		{#if org}
			<nav aria-label="Portal" class="nav">
				<a href={`/portal/${org}/invoices`}>Invoices</a>
				{#if currentOrg?.role === 'tenant'}
					<a href={`/portal/${org}/leases`}>Leases</a>
				{/if}
			</nav>
		{/if}
		<div class="topbar-actions">
			<button
				type="button"
				class="icon-button"
				onclick={() => theme.cycle()}
				aria-label={`Theme: ${themeLabel} (click to change)`}
			>
				{themeLabel}
			</button>
			<button type="button" onclick={logout}>Logout</button>
		</div>
	</header>

	<main id="main-content">
		{#key org}
			{@render children()}
		{/key}
	</main>
</div>

<style>
	.portal-shell {
		min-height: 100vh;
		display: flex;
		flex-direction: column;
	}
	.topbar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2) var(--space-4);
		padding: var(--space-3) var(--space-4);
		background: var(--surface);
		border-bottom: 1px solid var(--border);
	}
	.org-name {
		font-weight: 600;
	}
	.nav {
		display: flex;
		gap: var(--space-3);
	}
	.nav a {
		color: var(--text);
	}
	.topbar-actions {
		margin-left: auto;
		display: flex;
		align-items: center;
		gap: var(--space-2);
	}
	.icon-button,
	.topbar-actions button {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius-md);
		padding: var(--space-2) var(--space-3);
		cursor: pointer;
	}
	main {
		flex: 1;
		padding: var(--space-6) var(--space-4);
		min-width: 0;
	}

	@media (max-width: 374px) {
		.org-name {
			display: none;
		}
	}
</style>

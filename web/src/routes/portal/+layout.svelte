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

	// A user can hold both a portal membership and a staff membership (e.g.
	// an owner who's also a tenant of their own property) - give them a way
	// back to their staff org without having to sign out of the portal
	// shell first. !o.portal is a staff org (the API omits `portal` for
	// owner/staff/bookkeeper memberships).
	const staffOrg = $derived(session.me?.orgs.find((o) => !o.portal) ?? null);

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
			{#if staffOrg}
				<a href={`/${staffOrg.slug}`}>Back to {staffOrg.name}</a>
			{/if}
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

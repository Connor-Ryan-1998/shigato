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

<a href="#main-content" class="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50 focus:rounded-box focus:bg-base-100 focus:p-2">Skip to content</a>
<div class="flex min-h-screen flex-col bg-base-200">
	<header class="navbar flex-wrap gap-2 border-b border-base-300 bg-base-100 px-4">
		<span class="hidden font-semibold min-[375px]:inline">{currentOrg?.name ?? 'Portal'}</span>
		{#if org}
			<nav aria-label="Portal" class="flex gap-1">
				<a class="btn btn-ghost btn-sm" href={`/portal/${org}/invoices`} aria-current={page.url.pathname.startsWith(`/portal/${org}/invoices`) ? 'page' : undefined}>Invoices</a>
				{#if currentOrg?.role === 'tenant'}
					<a class="btn btn-ghost btn-sm" href={`/portal/${org}/leases`} aria-current={page.url.pathname.startsWith(`/portal/${org}/leases`) ? 'page' : undefined}>Leases</a>
				{/if}
			</nav>
		{/if}
		<div class="ml-auto flex flex-wrap items-center gap-2">
			{#if staffOrg}
				<a class="btn btn-ghost btn-sm" href={`/${staffOrg.slug}`}>Back to {staffOrg.name}</a>
			{/if}
			<button
				type="button"
				class="btn btn-ghost btn-sm"
				onclick={() => theme.cycle()}
				aria-label={`Theme: ${themeLabel} (click to change)`}
			>
				{themeLabel}
			</button>
			<button type="button" class="btn btn-outline btn-sm" onclick={logout}>Logout</button>
		</div>
	</header>

	<main id="main-content" class="min-w-0 flex-1 p-4 md:p-6">
		{#key org}
			{@render children()}
		{/key}
	</main>
</div>

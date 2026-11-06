<script lang="ts">
	import type { Component } from 'svelte';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { api, ApiError } from '../../lib/api';
	import PageHeader from '../../lib/components/PageHeader.svelte';
	import EmptyState from '../../lib/components/EmptyState.svelte';
	import Button from '../../lib/components/Button.svelte';
	import DashboardCore, { type CoreDashboard } from '../../lib/modules/core/DashboardCore.svelte';
	import { property } from '../../lib/modules/property/manifest';
	import { timesheets } from '../../lib/modules/timesheets/manifest';
	import { reconciliation } from '../../lib/modules/reconciliation/manifest';

	// Dashboard response shape (GET /api/v1/orgs/{org}/dashboard): "modules"
	// only contains the sections the caller's role may see, keyed by module
	// name - each value's real shape is the matching DashboardCard's own
	// exported type, but this page only passes it through unopened.
	type DashboardResponse = {
		core: CoreDashboard;
		modules: Record<string, unknown>;
	};

	// Order matches the sidebar's module group order (registry.ts).
	const moduleManifests = [property, timesheets, reconciliation];

	type LoadedCard = { name: string; Component: Component; data: unknown };

	let status = $state<'loading' | 'error' | 'ready'>('loading');
	let errorMessage = $state('');
	let core = $state<CoreDashboard | null>(null);
	let cards = $state<LoadedCard[]>([]);

	async function load() {
		status = 'loading';
		errorMessage = '';
		try {
			const org = page.params.org;
			const res = await api<DashboardResponse>('GET', `/api/v1/orgs/${org}/dashboard`);
			core = res.core;

			const loaded: LoadedCard[] = [];
			for (const manifest of moduleManifests) {
				const section = res.modules[manifest.name];
				// A section is absent when its module is disabled, or the
				// caller lacks the read permission for it - render nothing
				// rather than a card for data that isn't there.
				if (section === undefined || !manifest.dashboardCard) continue;
				const mod = await manifest.dashboardCard();
				loaded.push({ name: manifest.name, Component: mod.default, data: section });
			}
			cards = loaded;
			status = 'ready';
		} catch (e) {
			errorMessage = e instanceof ApiError ? e.message : 'Something went wrong.';
			status = 'error';
		}
	}

	onMount(load);

	// An org with zero invoices of any status (draft, open, paid, void,
	// uncollectible) has nothing to work with yet, so show the onboarding
	// empty state instead of three zero tiles. invoice_count (unlike
	// open/overdue/draft_count) isn't scoped to one status, so a settled
	// org (every invoice paid or void) correctly stays on the normal tiles
	// view instead of being mistaken for a brand-new one.
	const isEmpty = $derived(core !== null && core.invoice_count === 0);
</script>

<PageHeader title="Dashboard" />

{#if status === 'loading'}
	<div class="skeleton" role="status" aria-live="polite">
		<span class="visually-hidden">Loading dashboard…</span>
		<div class="skeleton-tiles" aria-hidden="true">
			<div class="skeleton-tile"></div>
			<div class="skeleton-tile"></div>
			<div class="skeleton-tile"></div>
		</div>
	</div>
{:else if status === 'error'}
	<EmptyState title="Couldn't load the dashboard" description={errorMessage}>
		{#snippet action()}
			<Button onclick={load}>Retry</Button>
		{/snippet}
	</EmptyState>
{:else if isEmpty}
	<EmptyState title="No invoices yet — create one" />
{:else if core}
	<DashboardCore {core} org={page.params.org ?? ''} />
	{#if cards.length > 0}
		<div class="module-cards">
			{#each cards as { name, Component, data } (name)}
				<Component {data} org={page.params.org ?? ''} />
			{/each}
		</div>
	{/if}
{/if}

<style>
	.visually-hidden {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip: rect(0, 0, 0, 0);
		white-space: nowrap;
	}
	.skeleton-tiles {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
		gap: var(--space-4);
	}
	.skeleton-tile {
		height: 6rem;
		border-radius: var(--radius-lg);
		background: linear-gradient(
			90deg,
			var(--surface) 25%,
			var(--surface-raised) 50%,
			var(--surface) 75%
		);
		background-size: 200% 100%;
		animation: shimmer 1.4s ease-in-out infinite;
	}
	@media (prefers-reduced-motion: reduce) {
		.skeleton-tile {
			animation: none;
		}
	}
	@keyframes shimmer {
		0% {
			background-position: 200% 0;
		}
		100% {
			background-position: -200% 0;
		}
	}
	.module-cards {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(16rem, 1fr));
		gap: var(--space-4);
	}
</style>

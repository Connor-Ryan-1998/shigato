<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../../lib/api';
	import { date, money } from '../../../lib/format';
	import { session } from '../../../lib/session.svelte';
	import PageHeader from '../../../lib/components/PageHeader.svelte';
	import Button from '../../../lib/components/Button.svelte';
	import StatusBadge from '../../../lib/components/StatusBadge.svelte';
	import EmptyState from '../../../lib/components/EmptyState.svelte';
	import Toast from '../../../lib/components/Toast.svelte';
	import ProjectDialog from '../../../lib/modules/timesheets/ProjectDialog.svelte';
	import {
		BILLING_LABELS,
		STATUS_LABELS,
		STATUS_TONES,
		type Project,
		type ProjectStatus
	} from '../../../lib/modules/timesheets/types';

	type Contact = { id: number; name: string; kind: string };

	const org = $derived(page.params.org ?? '');
	const membership = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(membership?.perms.includes('timesheets_read') ?? false);
	const isAdmin = $derived(membership?.perms.includes('timesheets_admin') ?? false);

	let projects = $state<Project[]>([]);
	let contacts = $state<Contact[]>([]);
	let loaded = $state('');
	let error = $state('');
	let search = $state('');
	let statusFilter = $state<'active' | 'all' | ProjectStatus>('active');
	let creating = $state(false);
	let seq = 0;

	const customer = $derived(new Map(contacts.map((c) => [c.id, c.name])));
	const visible = $derived.by(() => {
		const q = search.trim().toLowerCase();
		return projects.filter((p) => {
			if (statusFilter === 'active' ? !p.active : statusFilter !== 'all' && p.status !== statusFilter) return false;
			if (!q) return true;
			return [p.name, customer.get(p.client_contact_id) ?? '', p.description].some((s) => s.toLowerCase().includes(q));
		});
	});

	async function load(slug: string) {
		const s = ++seq;
		loaded = '';
		error = '';
		try {
			const [p, c] = await Promise.all([
				api<Project[]>('GET', `/api/v1/orgs/${slug}/projects`),
				// Customer names need invoices_read; without it the list
				// still works, just without names.
				api<Contact[]>('GET', `/api/v1/orgs/${slug}/contacts`).catch(() => [] as Contact[])
			]);
			if (s !== seq) return;
			projects = p;
			contacts = c;
			loaded = slug;
		} catch (e) {
			if (s === seq) error = e instanceof ApiError ? e.message : 'Something went wrong.';
		}
	}

	$effect(() => {
		const slug = org;
		if (canRead && slug) untrack(() => load(slug));
		return () => {
			seq++;
		};
	});
</script>

<PageHeader title="Projects">
	{#snippet actions()}
		{#if isAdmin && loaded === org}
			<Button onclick={() => (creating = true)}>New project</Button>
		{/if}
	{/snippet}
</PageHeader>

{#if !canRead}
	<p role="alert">You do not have permission to view projects.</p>
{:else if loaded !== org}
	{#if !error}<p role="status">Loading projects…</p>{/if}
{:else}
	<div class="mb-4 flex flex-wrap items-center gap-3">
		<label class="input w-full max-w-xs">
			<span class="sr-only">Search projects</span>
			<input type="search" placeholder="Search projects" bind:value={search} />
		</label>
		<label class="select w-auto">
			<span class="label">Status</span>
			<select bind:value={statusFilter} aria-label="Status filter">
				<option value="active">Active</option>
				{#each Object.entries(STATUS_LABELS) as [value, label] (value)}
					<option {value}>{label}</option>
				{/each}
				<option value="all">All</option>
			</select>
		</label>
	</div>

	{#if visible.length === 0}
		<EmptyState title="No projects" description={projects.length ? 'Nothing matches this search.' : 'Create a project to start logging time.'} />
	{:else}
		<div class="overflow-x-auto rounded-box border border-base-300 bg-base-100">
			<table class="table">
				<thead>
					<tr><th>Name</th><th>Customer</th><th>Status</th><th>Billing</th><th class="text-right">Base rate</th><th>Dates</th></tr>
				</thead>
				<tbody>
					{#each visible as p (p.id)}
						<tr class="hover:bg-base-200">
							<td><a class="link link-primary font-medium" href={`/${org}/projects/${p.id}`}>{p.name}</a></td>
							<td>{customer.get(p.client_contact_id) ?? '—'}</td>
							<td><StatusBadge status={STATUS_LABELS[p.status]} tone={STATUS_TONES[p.status]} /></td>
							<td>{BILLING_LABELS[p.billing_method]}</td>
							<td class="text-right tabular-nums">{money(p.hourly_rate)}/h</td>
							<td class="whitespace-nowrap">
								{p.start_date ? date(p.start_date) : '—'} – {p.end_date ? date(p.end_date) : '—'}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
{/if}

<ProjectDialog bind:open={creating} {org} {contacts} onsaved={(p) => goto(`/${org}/projects/${p.id}`)} />

{#if error}
	<Toast message={error} tone="danger" onDismiss={() => (error = '')} />
{/if}

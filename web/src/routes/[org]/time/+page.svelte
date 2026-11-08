<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { api, ApiError } from '../../../lib/api';
	import { todayLocal } from '../../../lib/format';
	import { session } from '../../../lib/session.svelte';
	import Toast from '../../../lib/components/Toast.svelte';
	import EntriesTable from '../../../lib/modules/timesheets/EntriesTable.svelte';
	import LogTimeDialog from '../../../lib/modules/timesheets/LogTimeDialog.svelte';
	import {
		PERIOD_LABELS,
		periodRange,
		type Entry,
		type Period,
		type Project
	} from '../../../lib/modules/timesheets/types';

	type Status = 'unbilled' | 'billed' | 'all';
	const STATUS_TITLES: Record<Status, string> = {
		unbilled: 'Timesheets Yet to be Invoiced',
		billed: 'Timesheets Invoiced',
		all: 'All Timesheets'
	};

	const org = $derived(page.params.org ?? '');
	const membership = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(membership?.perms.includes('timesheets_read') ?? false);
	const canWrite = $derived(membership?.perms.includes('timesheets_write') ?? false);
	const isAdmin = $derived(membership?.perms.includes('timesheets_admin') ?? false);

	let status = $state<Status>('unbilled');
	let period = $state<Period>('all');
	// Customer/project/user filter client-side over the status+period
	// result, so their options are exactly what that result contains.
	// ponytail: fine until an org has thousands of entries per period; then
	// push these to the server's client_id/project_id/user_id params.
	let customer = $state('');
	let projectFilter = $state('');
	let user = $state('');

	let entries = $state<Entry[]>([]);
	let projects = $state<Project[]>([]);
	let loadedView = $state('');
	let error = $state('');
	let busy = $state(false);
	let dialogOpen = $state(false);
	let editing = $state<Entry | null>(null);
	let seq = 0;

	const view = $derived(`${org}/${status}/${period}`);
	const ready = $derived(canRead && loadedView === view);

	function options(pairs: [string, string][]) {
		return [...new Map(pairs)].sort((a, b) => a[1].localeCompare(b[1]));
	}
	const customerOptions = $derived(options(entries.map((e) => [String(e.client_contact_id), e.client_name])));
	const projectOptions = $derived(options(entries.map((e) => [String(e.project_id), e.project_name])));
	const userOptions = $derived(options(entries.map((e) => [String(e.user_id), e.user_name])));
	const shown = $derived(
		entries.filter(
			(e) =>
				(customer === '' || String(e.client_contact_id) === customer) &&
				(projectFilter === '' || String(e.project_id) === projectFilter) &&
				(user === '' || String(e.user_id) === user)
		)
	);

	function fail(e: unknown) {
		error = e instanceof ApiError ? e.message : 'Something went wrong.';
	}

	async function load() {
		const s = ++seq;
		const scope = view;
		const params = new URLSearchParams();
		if (status !== 'all') params.set(status, 'true');
		const range = periodRange(period, todayLocal());
		if (range) {
			params.set('from', range.from);
			params.set('to', range.to);
		}
		const base = `/api/v1/orgs/${org}`;
		try {
			const [e, p] = await Promise.all([
				api<{ entries: Entry[] }>('GET', `${base}/time-entries?${params}`),
				api<Project[]>('GET', `${base}/projects`)
			]);
			if (s !== seq) return;
			// Newest first, like the screenshots' list.
			entries = e.entries.slice().reverse();
			projects = p;
			loadedView = scope;
		} catch (e) {
			if (s === seq) fail(e);
		}
	}

	// Reset filters on org switch.
	$effect(() => {
		org;
		untrack(() => {
			customer = projectFilter = user = '';
			entries = [];
			error = '';
		});
	});

	$effect(() => {
		if (canRead && view) untrack(load);
		return () => {
			seq++;
		};
	});

	function editable(e: Entry) {
		return canWrite && !e.invoice_line_id && (isAdmin || e.user_id === session.me?.user.id);
	}

	function openNew() {
		editing = null;
		dialogOpen = true;
	}
	function openEdit(e: Entry) {
		editing = e;
		dialogOpen = true;
	}

	async function remove(e: Entry) {
		if (busy || !editable(e) || !confirm('Delete this time entry?')) return;
		busy = true;
		error = '';
		const s = seq;
		try {
			await api('DELETE', `/api/v1/orgs/${org}/time-entries/${e.id}`);
			if (s === seq) await load();
		} catch (err) {
			fail(err);
		} finally {
			busy = false;
		}
	}
</script>

<header class="mb-4 flex flex-wrap items-center justify-between gap-4">
	<div class="dropdown">
		<div tabindex="0" role="button" class="flex cursor-pointer items-center gap-2 text-2xl font-bold">
			<h1 class="m-0">{STATUS_TITLES[status]}</h1>
			<span aria-hidden="true" class="text-primary">▾</span>
		</div>
		<ul class="menu dropdown-content z-10 w-64 rounded-box bg-base-100 p-2 shadow" aria-label="Timesheet status">
			{#each Object.entries(STATUS_TITLES) as [value, title] (value)}
				<li>
					<button
						type="button"
						class={{ 'menu-active': status === value }}
						onclick={(ev) => {
							status = value as Status;
							(ev.currentTarget as HTMLElement).blur();
						}}>{title}</button
					>
				</li>
			{/each}
		</ul>
	</div>
	{#if canWrite}
		<button type="button" class="btn btn-primary" onclick={openNew}>+ New Log Entry</button>
	{/if}
</header>

{#if !canRead}
	<p role="alert">You do not have permission to view timesheets.</p>
{:else}
	<div class="mb-4 flex flex-wrap items-end gap-3 rounded-box border border-base-300 bg-base-100 p-3">
		<span class="self-center text-sm font-semibold text-base-content/70 uppercase">View by</span>
		<label class="select select-sm w-auto">
			<span class="label">Period</span>
			<select bind:value={period} aria-label="Period">
				{#each Object.entries(PERIOD_LABELS) as [value, label] (value)}
					<option {value}>{label}</option>
				{/each}
			</select>
		</label>
		<label class="select select-sm w-auto">
			<span class="label">Customer</span>
			<select bind:value={customer} aria-label="Customer">
				<option value="">All</option>
				{#each customerOptions as [value, label] (value)}<option {value}>{label}</option>{/each}
			</select>
		</label>
		<label class="select select-sm w-auto">
			<span class="label">Project</span>
			<select bind:value={projectFilter} aria-label="Project">
				<option value="">All</option>
				{#each projectOptions as [value, label] (value)}<option {value}>{label}</option>{/each}
			</select>
		</label>
		<label class="select select-sm w-auto">
			<span class="label">User</span>
			<select bind:value={user} aria-label="User">
				<option value="">All</option>
				{#each userOptions as [value, label] (value)}<option {value}>{label}</option>{/each}
			</select>
		</label>
	</div>

	{#if !ready && !error}
		<p role="status" class="flex items-center gap-2">
			<span class="loading loading-spinner loading-sm" aria-hidden="true"></span>Loading entries…
		</p>
	{:else if ready}
		<EntriesTable
			entries={shown}
			{org}
			{editable}
			onedit={openEdit}
			ondelete={remove}
			{busy}
		/>
	{/if}

	<LogTimeDialog bind:open={dialogOpen} {org} {projects} entry={editing} onsaved={load} />
{/if}

{#if error}
	<div class="toast toast-end">
		<Toast message={error} tone="danger" onDismiss={() => (error = '')} />
	</div>
{/if}

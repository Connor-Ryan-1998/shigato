<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../../../lib/api';
	import { date, money, parseCents, todayLocal } from '../../../../lib/format';
	import { session } from '../../../../lib/session.svelte';
	import Button from '../../../../lib/components/Button.svelte';
	import Dialog from '../../../../lib/components/Dialog.svelte';
	import Field from '../../../../lib/components/Field.svelte';
	import StatusBadge from '../../../../lib/components/StatusBadge.svelte';
	import Toast from '../../../../lib/components/Toast.svelte';
	import ProjectDialog from '../../../../lib/modules/timesheets/ProjectDialog.svelte';
	import LogTimeDialog from '../../../../lib/modules/timesheets/LogTimeDialog.svelte';
	import EntriesTable from '../../../../lib/modules/timesheets/EntriesTable.svelte';
	import HoursChart from '../../../../lib/modules/timesheets/HoursChart.svelte';
	import {
		BILLING_LABELS,
		PERIOD_LABELS,
		STATUS_LABELS,
		STATUS_TONES,
		billingStatus,
		entryValue,
		hhmm,
		periodRange,
		type Entry,
		type Period,
		type Project,
		type Task
	} from '../../../../lib/modules/timesheets/types';

	type Contact = { id: number; name: string; kind: string };
	type Totals = { logged: number; billed: number; unbilled: number };

	const org = $derived(page.params.org ?? '');
	const id = $derived(page.params.id ?? '');
	const base = $derived(`/api/v1/orgs/${org}`);
	const membership = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(membership?.perms.includes('timesheets_read') ?? false);
	const canWrite = $derived(membership?.perms.includes('timesheets_write') ?? false);
	const isAdmin = $derived(membership?.perms.includes('timesheets_admin') ?? false);

	let project = $state<Project | null>(null);
	let contacts = $state<Contact[]>([]);
	let tasks = $state<Task[]>([]);
	let entries = $state<Entry[]>([]);
	let loaded = $state('');
	let error = $state('');
	let busy = $state(false);
	let tab = $state<'overview' | 'timesheet'>(
		page.url?.searchParams.get('tab') === 'timesheet' ? 'timesheet' : 'overview'
	);

	// Timesheet tab
	let tsStatus = $state<'unbilled' | 'billed' | 'all'>('unbilled');
	let tsPeriod = $state<Period>('all');
	let tsEntries = $state<Entry[]>([]);
	let tsLoaded = $state('');

	// Dialogs
	let editingProject = $state(false);
	let logOpen = $state(false);
	let editingEntry = $state<Entry | null>(null);
	let taskOpen = $state(false);
	let taskDraft = $state<{ id: number | null; name: string; rate: string; active: boolean }>({ id: null, name: '', rate: '', active: true });
	let taskError = $state('');

	let seq = 0;
	let tsSeq = 0;
	const key = $derived(`${org}/${id}`);
	const tsKey = $derived(`${key}/${tsStatus}/${tsPeriod}`);

	const customer = $derived(contacts.find((c) => c.id === project?.client_contact_id)?.name);
	const fixed = $derived(project?.billing_method === 'fixed');

	function add(t: Totals, e: Entry) {
		t.logged += e.minutes;
		const s = billingStatus(e);
		if (s === 'billed') t.billed += e.minutes;
		if (s === 'unbilled') t.unbilled += e.minutes;
	}
	function group(by: (e: Entry) => number, name: (e: Entry) => string) {
		const m = new Map<number, Totals & { name: string }>();
		for (const e of entries) {
			const k = by(e);
			if (!m.has(k)) m.set(k, { name: name(e), logged: 0, billed: 0, unbilled: 0 });
			add(m.get(k)!, e);
		}
		return m;
	}
	const users = $derived([...group((e) => e.user_id, (e) => e.user_name).values()]);
	const byTask = $derived(group((e) => e.task_id, (e) => e.task_name));
	const value = (pick: (e: Entry) => boolean) =>
		entries.filter(pick).reduce((sum, e) => sum + entryValue(e), 0);
	const stats = $derived.by(() => {
		const t: Totals = { logged: 0, billed: 0, unbilled: 0 };
		for (const e of entries) add(t, e);
		return [
			{ label: 'Logged', minutes: t.logged, cents: value(() => true) },
			{ label: 'Billable', minutes: fixed ? 0 : t.logged, cents: fixed ? 0 : value(() => true) },
			{ label: 'Billed', minutes: t.billed, cents: value((e) => billingStatus(e) === 'billed') },
			{ label: 'Unbilled', minutes: t.unbilled, cents: value((e) => billingStatus(e) === 'unbilled') }
		];
	});

	function fail(e: unknown) {
		error = e instanceof ApiError ? e.message : 'Something went wrong.';
	}

	async function load() {
		const s = ++seq;
		const k = key;
		try {
			const [p, t, en, c] = await Promise.all([
				api<Project>('GET', `${base}/projects/${id}`),
				api<Task[]>('GET', `${base}/projects/${id}/tasks`),
				api<{ entries: Entry[] }>('GET', `${base}/time-entries?project_id=${id}`),
				// Customer name needs invoices_read; degrade without it.
				api<Contact[]>('GET', `${base}/contacts`).catch(() => [] as Contact[])
			]);
			if (s !== seq || k !== key) return;
			project = p;
			tasks = t;
			entries = en.entries;
			contacts = c;
			loaded = k;
		} catch (e) {
			if (s === seq && k === key) fail(e);
		}
	}

	async function loadTimesheet() {
		const s = ++tsSeq;
		const k = tsKey;
		const range = periodRange(tsPeriod, todayLocal());
		const q = new URLSearchParams({ project_id: id });
		if (tsStatus !== 'all') q.set(tsStatus, 'true');
		if (range) {
			q.set('from', range.from);
			q.set('to', range.to);
		}
		try {
			const res = await api<{ entries: Entry[] }>('GET', `${base}/time-entries?${q}`);
			if (s !== tsSeq || k !== tsKey) return;
			tsEntries = res.entries;
			tsLoaded = k;
		} catch (e) {
			if (s === tsSeq && k === tsKey) fail(e);
		}
	}

	$effect(() => {
		key;
		if (!canRead) return;
		untrack(() => {
			loaded = '';
			project = null;
			error = '';
			load();
		});
		return () => {
			seq++;
		};
	});

	$effect(() => {
		tsKey;
		if (canRead && tab === 'timesheet') untrack(loadTimesheet);
		return () => {
			tsSeq++;
		};
	});

	function refresh() {
		load();
		if (tab === 'timesheet') loadTimesheet();
	}

	function selectTab(t: 'overview' | 'timesheet') {
		tab = t;
		goto(`?tab=${t}`, { replaceState: true, shallow: true });
	}

	function editable(e: Entry) {
		return canWrite && !e.invoice_line_id && (isAdmin || e.user_id === session.me?.user.id);
	}

	async function run(fn: () => Promise<unknown>) {
		if (busy) return;
		busy = true;
		error = '';
		try {
			await fn();
			refresh();
		} catch (e) {
			fail(e);
		} finally {
			busy = false;
		}
	}

	function deleteEntry(e: Entry) {
		if (editable(e) && confirm('Delete this time entry?')) run(() => api('DELETE', `${base}/time-entries/${e.id}`));
	}

	function openTask(t?: Task) {
		taskError = '';
		taskDraft = t
			? { id: t.id, name: t.name, rate: t.hourly_rate == null ? '' : (t.hourly_rate / 100).toFixed(2), active: t.active }
			: { id: null, name: '', rate: '', active: true };
		taskOpen = true;
	}

	async function saveTask(e: SubmitEvent) {
		e.preventDefault();
		let rate: number | null = null;
		if (taskDraft.rate.trim() !== '') {
			rate = parseCents(taskDraft.rate);
			if (rate === null || rate <= 0) {
				taskError = 'Task hourly rate must be greater than zero.';
				return;
			}
		}
		busy = true;
		taskError = '';
		try {
			if (taskDraft.id === null) {
				await api('POST', `${base}/projects/${id}/tasks`, { name: taskDraft.name, hourly_rate: rate ?? undefined });
			} else {
				await api('PUT', `${base}/tasks/${taskDraft.id}`, { name: taskDraft.name, hourly_rate: rate, active: taskDraft.active });
			}
			taskOpen = false;
			refresh();
		} catch (err) {
			taskError = err instanceof ApiError ? err.message : 'Something went wrong.';
		} finally {
			busy = false;
		}
	}

	function setTaskActive(t: Task, active: boolean) {
		if (isAdmin) run(() => api('PUT', `${base}/tasks/${t.id}`, { name: t.name, hourly_rate: t.hourly_rate ?? null, active }));
	}
</script>

{#if !canRead}
	<p role="alert">You do not have permission to view projects.</p>
{:else if !project || loaded !== key}
	{#if !error}<p role="status">Loading project…</p>{/if}
{:else}
	<header class="mb-4 flex flex-wrap items-center gap-3">
		<a class="btn btn-square btn-ghost btn-sm" href={`/${org}/projects`} aria-label="Back to projects">‹</a>
		<h1 class="m-0 text-2xl font-bold">{project.name}</h1>
		<StatusBadge status={STATUS_LABELS[project.status]} tone={STATUS_TONES[project.status]} />
		<div class="ml-auto flex gap-2">
			{#if isAdmin}<Button variant="secondary" onclick={() => (editingProject = true)}>Edit</Button>{/if}
			{#if canWrite && project.active}
				<Button onclick={() => { editingEntry = null; logOpen = true; }}>Log Time</Button>
			{/if}
		</div>
	</header>

	<div role="tablist" class="tabs tabs-border mb-4">
		<button type="button" role="tab" class={['tab', tab === 'overview' && 'tab-active']} aria-selected={tab === 'overview'} onclick={() => selectTab('overview')}>Overview</button>
		<button type="button" role="tab" class={['tab', tab === 'timesheet' && 'tab-active']} aria-selected={tab === 'timesheet'} onclick={() => selectTab('timesheet')}>Timesheet</button>
	</div>

	{#if tab === 'overview'}
		<div class="grid gap-4 lg:grid-cols-[20rem_1fr]">
			<section class="card bg-base-100 shadow-sm" aria-label="Project details">
				<dl class="card-body gap-3">
					<div><dt class="text-sm text-base-content/70">Customer</dt><dd class="text-lg font-semibold text-primary">{customer ?? '—'}</dd></div>
					<div><dt class="text-sm text-base-content/70">Billing method</dt><dd>{BILLING_LABELS[project.billing_method]}</dd></div>
					<div><dt class="text-sm text-base-content/70">Base rate</dt><dd class="tabular-nums">{money(project.hourly_rate)}/h</dd></div>
					<div><dt class="text-sm text-base-content/70">Dates</dt><dd>{project.start_date ? date(project.start_date) : '—'} – {project.end_date ? date(project.end_date) : '—'}</dd></div>
					{#if project.description}
						<div><dt class="text-sm text-base-content/70">Description</dt><dd class="whitespace-pre-wrap">{project.description}</dd></div>
					{/if}
				</dl>
			</section>
			<section class="card bg-base-100 shadow-sm" aria-label="Project hours">
				<div class="card-body">
					<HoursChart {entries} />
					<div class="stats stats-vertical mt-4 w-full border-t border-base-300 sm:stats-horizontal">
						{#each stats as s (s.label)}
							<div class="stat place-items-center">
								<div class="stat-title">{s.label} hours</div>
								<div class="stat-value text-primary text-2xl tabular-nums">{hhmm(s.minutes)}</div>
								<div class="stat-desc text-base tabular-nums">{money(s.cents)}</div>
							</div>
						{/each}
					</div>
					{#if fixed}<p class="text-sm text-base-content/70">Fixed-price project: time is tracked but not billed by billing runs.</p>{/if}
				</div>
			</section>
		</div>

		<section class="mt-6" aria-labelledby="users-heading">
			<h2 id="users-heading" class="mb-2 text-lg font-semibold">Users</h2>
			<div class="overflow-x-auto rounded-box border border-base-300 bg-base-100">
				<table class="table">
					<thead><tr><th>Name</th><th class="text-right">Logged</th><th class="text-right">Billed</th><th class="text-right">Unbilled</th></tr></thead>
					<tbody>
						{#each users as u (u.name)}
							<tr><td>{u.name}</td><td class="text-right tabular-nums">{hhmm(u.logged)}</td><td class="text-right tabular-nums">{hhmm(u.billed)}</td><td class="text-right tabular-nums">{hhmm(u.unbilled)}</td></tr>
						{:else}
							<tr><td colspan="4" class="text-base-content/70">No time logged yet.</td></tr>
						{/each}
					</tbody>
				</table>
			</div>
		</section>

		<section class="mt-6" aria-labelledby="tasks-heading">
			<div class="mb-2 flex items-center justify-between">
				<h2 id="tasks-heading" class="text-lg font-semibold">Project tasks</h2>
				{#if isAdmin}<button type="button" class="btn btn-ghost btn-sm text-primary" disabled={busy} onclick={() => openTask()}>+ Add task</button>{/if}
			</div>
			<div class="overflow-x-auto rounded-box border border-base-300 bg-base-100">
				<table class="table">
					<thead><tr><th>Name</th><th>Rate</th><th class="text-right">Logged</th><th class="text-right">Billed</th><th class="text-right">Unbilled</th><th>Status</th><th><span class="sr-only">Actions</span></th></tr></thead>
					<tbody>
						{#each tasks as t (t.id)}
							{@const s = byTask.get(t.id) ?? { logged: 0, billed: 0, unbilled: 0 }}
							<tr>
								<td>{t.name}</td>
								<td class="tabular-nums">{t.hourly_rate != null ? `${money(t.hourly_rate)}/h` : 'Project rate'}</td>
								<td class="text-right tabular-nums">{hhmm(s.logged)}</td>
								<td class="text-right tabular-nums">{hhmm(s.billed)}</td>
								<td class="text-right tabular-nums">{hhmm(s.unbilled)}</td>
								<td><StatusBadge status={t.active ? 'Active' : 'Archived'} tone={t.active ? 'success' : 'warning'} /></td>
								<td class="whitespace-nowrap text-right">
									{#if isAdmin}
										<button type="button" class="btn btn-ghost btn-xs" disabled={busy} aria-label={`Edit task ${t.name}`} onclick={() => openTask(t)}>Edit</button>
										<button type="button" class="btn btn-ghost btn-xs" disabled={busy} onclick={() => setTaskActive(t, !t.active)}>{t.active ? 'Archive' : 'Restore'}</button>
									{/if}
								</td>
							</tr>
						{:else}
							<tr><td colspan="7" class="text-base-content/70">No tasks yet.</td></tr>
						{/each}
					</tbody>
				</table>
			</div>
		</section>
	{:else}
		<div class="mb-4 flex flex-wrap items-center gap-3">
			<span class="text-sm font-semibold uppercase text-base-content/70">View by</span>
			<label class="select select-sm w-auto">
				<span class="label">Status</span>
				<select bind:value={tsStatus} aria-label="Billing status">
					<option value="unbilled">Yet to invoice</option>
					<option value="billed">Invoiced</option>
					<option value="all">All</option>
				</select>
			</label>
			<label class="select select-sm w-auto">
				<span class="label">Period</span>
				<select bind:value={tsPeriod} aria-label="Period">
					{#each Object.entries(PERIOD_LABELS) as [value, label] (value)}<option {value}>{label}</option>{/each}
				</select>
			</label>
		</div>
		{#if tsLoaded !== tsKey}
			{#if !error}<p role="status">Loading entries…</p>{/if}
		{:else}
			<EntriesTable
				entries={tsEntries}
				{org}
				showProject={false}
				{editable}
				{busy}
				onedit={(e) => { editingEntry = e; logOpen = true; }}
				ondelete={deleteEntry}
			/>
		{/if}
	{/if}

	<ProjectDialog bind:open={editingProject} {org} {contacts} {project} onsaved={refresh} />
	<LogTimeDialog bind:open={logOpen} {org} projects={[project]} entry={editingEntry} projectId={project.id} onsaved={refresh} />
	<Dialog bind:open={taskOpen} title={taskDraft.id === null ? 'Add task' : 'Edit task'}>
		{#if taskOpen}
			<form class="flex flex-col gap-3" onsubmit={saveTask}>
				<Field label="Task name" id="task-name">
					<input class="input w-full" id="task-name" bind:value={taskDraft.name} disabled={busy} required />
				</Field>
				<Field label="Task hourly rate ($)" id="task-rate" hint="Leave blank to use the project rate.">
					<input class="input w-full" id="task-rate" inputmode="decimal" bind:value={taskDraft.rate} disabled={busy} />
				</Field>
				{#if taskError}<p class="alert alert-error alert-soft" role="alert">{taskError}</p>{/if}
				<div class="modal-action">
					<Button variant="secondary" disabled={busy} onclick={() => (taskOpen = false)}>Cancel</Button>
					<Button type="submit" disabled={busy}>{taskDraft.id === null ? 'Add task' : 'Save task'}</Button>
				</div>
			</form>
		{/if}
	</Dialog>
{/if}

{#if error}
	<Toast message={error} tone="danger" onDismiss={() => (error = '')} />
{/if}

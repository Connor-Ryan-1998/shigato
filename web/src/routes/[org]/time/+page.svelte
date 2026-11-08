<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { api, ApiError } from '../../../lib/api';
	import { money, todayLocal } from '../../../lib/format';
	import { session } from '../../../lib/session.svelte';
	import PageHeader from '../../../lib/components/PageHeader.svelte';
	import Field from '../../../lib/components/Field.svelte';
	import Select from '../../../lib/components/Select.svelte';
	import Button from '../../../lib/components/Button.svelte';
	import Toast from '../../../lib/components/Toast.svelte';

	type Contact = { id: number; name: string; kind: string };
	type Project = { id: number; name: string; client_contact_id: number; hourly_rate: number; gst_code: string; active: boolean };
	type Task = { id: number; name: string; hourly_rate?: number; active: boolean };
	type Entry = {
		id: number;
		task_id: number;
		user_id: number;
		work_date: string;
		minutes: number;
		note: string;
		invoice_line_id?: number;
	};

	const org = $derived(page.params.org);
	const base = $derived(`/api/v1/orgs/${page.params.org}`);
	const membership = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(membership?.perms.includes('timesheets_read') ?? false);
	const canWrite = $derived(membership?.perms.includes('timesheets_write') ?? false);
	const isAdmin = $derived(membership?.perms.includes('timesheets_admin') ?? false);

	let projects = $state<Project[]>([]);
	let projectId = $state('');
	let tasks = $state<Task[]>([]);
	let entries = $state<Entry[]>([]);
	let totalMinutes = $state(0);
	let contacts = $state<Contact[]>([]);
	let error = $state('');
	let loadedOrg = $state('');
	let loadedView = $state('');
	let busy = $state(false);
	let from = $state('');
	let to = $state('');
	let editing = $state<(Entry & { scope: string }) | null>(null);
	let showArchived = $state(false);
	let projectDraft = $state<(Project & { rate: string; scope: string }) | null>(null);
	let taskDraft = $state<(Task & { rate: string; scope: string }) | null>(null);
	let orgSeq = 0;
	let projectsSeq = 0;
	const view = $derived(`${org}/${projectId}/${from}/${to}`);
	const ready = $derived(canRead && loadedOrg === org);
	const detailsReady = $derived(ready && loadedView === view);
	const selectedProject = $derived(projects.find((p) => String(p.id) === projectId));
	const originalTaskId = $derived(entries.find((entry) => entry.id === editing?.id)?.task_id);
	const editTaskOptions = $derived(tasks.filter((t) => t.active || t.id === originalTaskId).map((t) => ({ value: String(t.id), label: t.name })));

	// Log-time form
	let taskId = $state('');
	let workDate = $state(todayLocal());
	let minutes = $state('60');
	let note = $state('');

	// Admin forms
	let newProjectName = $state('');
	let newProjectClient = $state('');
	let newProjectRate = $state('');
	let newTaskName = $state('');

	const projectOptions = $derived(projects.map((p) => ({ value: String(p.id), label: `${p.name}${p.active ? '' : ' (archived)'}` })));
	const taskOptions = $derived(
		tasks.filter((t) => t.active).map((t) => ({ value: String(t.id), label: t.name }))
	);
	const clientOptions = $derived(
		contacts.filter((c) => c.kind === 'client').map((c) => ({ value: String(c.id), label: c.name }))
	);
	const taskName = $derived(new Map(tasks.map((t) => [t.id, t.name])));

	function fail(e: unknown) {
		error = e instanceof ApiError ? e.message : 'Something went wrong.';
	}

	async function loadProjects() {
		const slug = org;
		const token = orgSeq;
		const request = ++projectsSeq;
		const all = showArchived;
		try {
			const result = await api<Project[]>('GET', `${base}/projects${all ? '' : '?active=true'}`);
			if (slug !== org || token !== orgSeq || request !== projectsSeq || all !== showArchived) return;
			projects = result;
			if (!projects.some((p) => String(p.id) === projectId)) {
				projectId = projects[0] ? String(projects[0].id) : '';
			}
		} catch (e) {
			if (slug !== org || token !== orgSeq || request !== projectsSeq || all !== showArchived) return;
			throw e;
		}
	}

	let seq = 0;
	async function loadProject(id: string, start = from, end = to) {
		const s = ++seq;
		const scope = `${org}/${id}/${start}/${end}`;
		const url = base;
		loadedView = '';
		editing = null;
		projectDraft = null;
		taskDraft = null;
		tasks = [];
		entries = [];
		totalMinutes = 0;
		if (id === '') {
			return;
		}
		if (start && end && start > end) {
			error = 'Entries from must not be after entries to.';
			return;
		}
		error = '';
		try {
			const [t, e] = await Promise.all([
				api<Task[]>('GET', `${url}/projects/${id}/tasks`),
				api<{ entries: Entry[]; total_minutes: number }>(
					'GET',
					`${url}/time-entries?project_id=${id}${start ? `&from=${start}` : ''}${end ? `&to=${end}` : ''}`
				)
			]);
			if (s !== seq || scope !== view) return;
			tasks = t;
			entries = e.entries;
			totalMinutes = e.total_minutes;
			loadedView = scope;
			if (!t.some((x) => x.active && String(x.id) === taskId)) {
				const first = t.find((x) => x.active);
				taskId = first ? String(first.id) : '';
			}
		} catch (e) {
			if (s === seq && scope === view) fail(e);
		}
	}

	async function initialize(slug: string, admin: boolean) {
		const token = ++orgSeq;
		loadedOrg = '';
		projects = [];
		projectId = '';
		tasks = [];
		entries = [];
		contacts = [];
		editing = null;
		from = '';
		to = '';
		busy = false;
		error = '';
		note = '';
		newProjectName = '';
		newProjectRate = '';
		newTaskName = '';
		showArchived = false;
		try {
			const [p, c] = await Promise.all([
				api<Project[]>('GET', `/api/v1/orgs/${slug}/projects?active=true`),
				admin ? api<Contact[]>('GET', `/api/v1/orgs/${slug}/contacts`) : Promise.resolve([])
			]);
			if (slug !== org || token !== orgSeq) return;
			projects = p;
			contacts = c;
			projectId = p[0] ? String(p[0].id) : '';
			newProjectClient = c.find((contact) => contact.kind === 'client')?.id.toString() ?? '';
			loadedOrg = slug;
		} catch (e) {
			if (slug === org && token === orgSeq) fail(e);
		}
	}
	$effect(() => {
		const slug = org;
		const allowed = canRead;
		const admin = isAdmin;
		untrack(() => { if (allowed && slug) initialize(slug, admin); });
		return () => { orgSeq++; seq++; };
	});

	$effect(() => {
		if (ready) loadProject(projectId, from, to);
		return () => { seq++; };
	});

	async function act(method: 'POST' | 'PUT' | 'DELETE', path: string, body?: unknown,
		after?: (result: Project) => void, refreshProjects = false) {
		if (!ready || busy) return;
		const scope = view;
		const token = orgSeq;
		busy = true;
		error = '';
		try {
			const result = await api<Project>(method, `${base}/${path}`, body);
			if (token !== orgSeq || scope !== view) return;
			after?.(result);
			if (refreshProjects) await loadProjects();
			if (token !== orgSeq) return;
			await loadProject(projectId);
		} catch (e) {
			if (token === orgSeq && scope === view) fail(e);
		} finally {
			if (token === orgSeq) busy = false;
		}
	}

	function logTime(e: SubmitEvent) {
		e.preventDefault();
		if (!canWrite || !detailsReady || !selectedProject?.active) return;
		act('POST', 'time-entries', {
				task_id: Number(taskId),
				work_date: workDate,
				minutes: Number(minutes),
				note: note || undefined
			}, () => { note = ''; });
	}

	function deleteEntry(id: number) {
		const entry = entries.find((e) => e.id === id);
		if (detailsReady && entry && editable(entry)) act('DELETE', `time-entries/${id}`);
	}
	function editable(entry: Entry) {
		return canWrite && !entry.invoice_line_id && (isAdmin || entry.user_id === session.me?.user.id);
	}
	function saveEntry(e: SubmitEvent) {
		e.preventDefault();
		if (!detailsReady || !editing || editing.scope !== view || !editable(editing)) return;
		act('PUT', `time-entries/${editing.id}`, {
			task_id: editing.task_id, work_date: editing.work_date, minutes: editing.minutes, note: editing.note
		}, () => { editing = null; });
	}

	function createProject(e: SubmitEvent) {
		e.preventDefault();
		if (!isAdmin) return;
		act('POST', 'projects', {
				name: newProjectName,
				client_contact_id: Number(newProjectClient),
				hourly_rate: Math.round(Number(newProjectRate) * 100)
			}, (p) => {
			newProjectName = '';
			newProjectRate = '';
			projectId = String(p.id);
		}, true);
	}

	function createTask(e: SubmitEvent) {
		e.preventDefault();
		if (!isAdmin || !detailsReady) return;
		act('POST', `projects/${projectId}/tasks`, { name: newTaskName }, () => { newTaskName = ''; });
	}

	function setTaskActive(t: Task, active: boolean) {
		if (isAdmin && detailsReady) act('PUT', `tasks/${t.id}`, { name: t.name, hourly_rate: t.hourly_rate ?? null, active });
	}

	function archiveProject() {
		if (isAdmin && detailsReady && selectedProject) act('PUT', `projects/${projectId}`, {
			name: selectedProject.name, hourly_rate: selectedProject.hourly_rate,
			gst_code: selectedProject.gst_code, active: !selectedProject.active
		}, undefined, true);
	}
	function saveProject(e: SubmitEvent) {
		e.preventDefault();
		if (!isAdmin || !detailsReady || !projectDraft || projectDraft.scope !== view) return;
		act('PUT', `projects/${projectDraft.id}`, {
			name: projectDraft.name, hourly_rate: Math.round(Number(projectDraft.rate) * 100),
			gst_code: projectDraft.gst_code, active: projectDraft.active
		}, () => { projectDraft = null; }, true);
	}
	function saveTask(e: SubmitEvent) {
		e.preventDefault();
		if (!isAdmin || !detailsReady || !taskDraft || taskDraft.scope !== view) return;
		if (taskDraft.rate !== '' && (!Number.isFinite(Number(taskDraft.rate)) || Math.round(Number(taskDraft.rate) * 100) <= 0)) {
			error = 'Task hourly rate must be greater than zero.';
			return;
		}
		act('PUT', `tasks/${taskDraft.id}`, {
			name: taskDraft.name, hourly_rate: taskDraft.rate === '' ? null : Math.round(Number(taskDraft.rate) * 100),
			active: taskDraft.active
		}, () => { taskDraft = null; });
	}
	async function changeArchived() {
		const slug = org;
		try { await loadProjects(); } catch (e) { if (slug === org) fail(e); }
	}

	function hours(m: number): string {
		return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, '0')}m`;
	}
</script>

<PageHeader title="Time" />

{#if !canRead}
	<p role="alert">You do not have permission to view timesheets.</p>
{:else if !ready}
	<p role="status">Loading projects…</p>
{:else}

<Field label="Project" id="time-project">
	<Select id="time-project" bind:value={projectId} options={projectOptions} />
</Field>
<label class="label my-2"><input class="checkbox" type="checkbox" bind:checked={showArchived} onchange={changeArchived} disabled={busy} /> Show archived projects</label>
<div class="flex flex-wrap items-end gap-4">
	<Field label="Entries from" id="entries-from"><input class="input" id="entries-from" type="date" bind:value={from} /></Field>
	<Field label="Entries to" id="entries-to"><input class="input" id="entries-to" type="date" bind:value={to} /></Field>
</div>
{#if projects.length === 0}
	<p>No {showArchived ? '' : 'active '}projects.{isAdmin ? ' Create one below.' : ''}</p>
{/if}

{#if projectId !== '' && !detailsReady && !error}<p role="status">Loading entries…</p>{/if}
{#if projectId !== '' && detailsReady}
	{#if canWrite && selectedProject?.active}
	<section class="mt-8" aria-labelledby="log-heading">
		<h2 id="log-heading" class="mb-3 text-lg font-semibold">Log time</h2>
		{#if taskOptions.length === 0}
			<p>This project has no active tasks.</p>
		{:else}
			<form onsubmit={logTime}>
				<fieldset class="fieldset rounded-box border border-base-300 bg-base-100 p-4 flex flex-wrap items-end gap-4" disabled={busy}>
				<legend class="fieldset-legend">Time entry details</legend>
				<Field label="Task" id="log-task">
					<Select id="log-task" bind:value={taskId} options={taskOptions} />
				</Field>
				<Field label="Date" id="log-date">
					<input class="input" id="log-date" type="date" bind:value={workDate} required />
				</Field>
				<Field label="Minutes" id="log-minutes">
					<input class="input" id="log-minutes" type="number" min="1" step="1" bind:value={minutes} required />
				</Field>
				<Field label="Note" id="log-note"><input class="input" id="log-note" bind:value={note} /></Field>
				<Button type="submit" disabled={busy}>Log time</Button>
				</fieldset>
			</form>
		{/if}
	</section>
	{/if}
	{#if editing && editing.scope === view}
		<form class="my-4" aria-label="Edit time entry" onsubmit={saveEntry}>
			<fieldset class="fieldset rounded-box border border-base-300 bg-base-100 p-4 flex flex-wrap items-end gap-4" disabled={busy}>
				<legend class="fieldset-legend">Edit time entry</legend>
				<Field label="Entry task" id="entry-task">
					<select class="select" id="entry-task" bind:value={editing.task_id}>{#each editTaskOptions as t}<option value={Number(t.value)}>{t.label}</option>{/each}</select>
				</Field>
				<Field label="Entry date" id="entry-date"><input class="input" id="entry-date" type="date" bind:value={editing.work_date} required /></Field>
				<Field label="Entry minutes" id="entry-minutes"><input class="input" id="entry-minutes" type="number" min="1" max="1440" step="1" bind:value={editing.minutes} required /></Field>
				<Field label="Entry note" id="entry-note"><input class="input" id="entry-note" maxlength="1000" bind:value={editing.note} /></Field>
				<Button type="submit" disabled={busy}>Save entry</Button>
				<Button variant="secondary" disabled={busy} onclick={() => { editing = null; }}>Cancel</Button>
			</fieldset>
		</form>
	{/if}

	<section class="mt-8" aria-labelledby="entries-heading">
		<h2 id="entries-heading" class="mb-3 text-lg font-semibold">Entries ({hours(totalMinutes)})</h2>
		{#if entries.length === 0}
			<p>No time logged on this project.</p>
		{:else}
			<div class="overflow-x-auto">
			<table class="table">
				<thead>
					<tr><th>Date</th><th>Task</th><th>Time</th><th>Note</th><th></th></tr>
				</thead>
				<tbody>
					{#each entries as e (e.id)}
						<tr>
							<td>{e.work_date}</td>
							<td>{taskName.get(e.task_id) ?? e.task_id}</td>
							<td>{hours(e.minutes)}</td>
							<td>{e.note}</td>
							<td>
								{#if e.invoice_line_id}
									Billed
								{:else if editable(e)}
									<Button disabled={busy} onclick={() => { editing = { ...e, scope: view }; }}>Edit entry {e.id}</Button>
									<Button disabled={busy} variant="danger" onclick={() => deleteEntry(e.id)}>Delete</Button>
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
			</div>
		{/if}
	</section>
{/if}

{#if isAdmin}
	<section class="mt-8" aria-labelledby="admin-heading">
		<h2 id="admin-heading" class="mb-3 text-lg font-semibold">Projects and tasks</h2>
		{#if projectId !== '' && detailsReady}
			{#if selectedProject}
				<p class="mb-2 text-base-content/70">Rate changes apply when unbilled time is invoiced. Existing billed invoices do not change.</p>
				<Button disabled={busy} onclick={() => { if (selectedProject) projectDraft = { ...selectedProject, rate: (selectedProject.hourly_rate / 100).toFixed(2), scope: view }; }}>Edit project</Button>
				{#if projectDraft && projectDraft.scope === view}
					<form class="my-4" aria-label="Edit project" onsubmit={saveProject}>
						<fieldset class="fieldset rounded-box border border-base-300 bg-base-100 p-4 flex flex-wrap items-end gap-4" disabled={busy}>
							<legend class="fieldset-legend">Edit project</legend>
							<Field label="Project name" id="edit-project-name"><input class="input" id="edit-project-name" bind:value={projectDraft.name} required /></Field>
							<Field label="Project hourly rate ($)" id="edit-project-rate"><input class="input" id="edit-project-rate" type="number" min="0.01" step="0.01" bind:value={projectDraft.rate} required /></Field>
							<Button type="submit" disabled={busy}>Save project</Button>
							<Button variant="secondary" disabled={busy} onclick={() => { projectDraft = null; }}>Cancel</Button>
						</fieldset>
					</form>
				{/if}
			{/if}
			<h3 class="mt-4 mb-2 font-semibold">Tasks</h3>
			<ul class="mb-4 flex flex-col gap-2">
				{#each tasks as t (t.id)}
					<li class="flex flex-wrap items-center gap-2">
						{t.name}{t.hourly_rate !== undefined ? ` (${money(t.hourly_rate)}/h)` : ''}
						<Button disabled={busy} onclick={() => { taskDraft = { ...t, rate: t.hourly_rate == null ? '' : (t.hourly_rate / 100).toFixed(2), scope: view }; }}>Edit task {t.id}</Button>
						<Button disabled={busy} variant="secondary" onclick={() => setTaskActive(t, !t.active)}>
							{t.active ? 'Archive' : 'Restore'}
						</Button>
					</li>
				{/each}
			</ul>
			{#if taskDraft && taskDraft.scope === view}
				<form class="my-4" aria-label="Edit task" onsubmit={saveTask}>
					<fieldset class="fieldset rounded-box border border-base-300 bg-base-100 p-4 flex flex-wrap items-end gap-4" disabled={busy}>
						<legend class="fieldset-legend">Edit task</legend>
						<Field label="Task name" id="edit-task-name"><input class="input" id="edit-task-name" bind:value={taskDraft.name} required /></Field>
						<Field label="Task hourly rate ($)" id="edit-task-rate" hint="Leave blank to use the project rate. Overrides must be greater than zero.">
							<input class="input" id="edit-task-rate" type="number" min="0.01" step="0.01" value={taskDraft.rate} oninput={(e) => { if (taskDraft) taskDraft.rate = e.currentTarget.value; }} />
						</Field>
						<Button type="submit" disabled={busy}>Save task</Button>
						<Button variant="secondary" disabled={busy} onclick={() => { taskDraft = null; }}>Cancel</Button>
					</fieldset>
				</form>
			{/if}
			<form onsubmit={createTask}>
				<fieldset class="fieldset rounded-box border border-base-300 bg-base-100 p-4 flex flex-wrap items-end gap-4" disabled={busy}>
				<legend class="fieldset-legend">New task details</legend>
				<Field label="New task" id="new-task">
					<input class="input" id="new-task" bind:value={newTaskName} required />
				</Field>
				<Button type="submit" disabled={busy}>Add task</Button>
				</fieldset>
			</form>
			<p class="my-4"><Button disabled={busy} variant={selectedProject?.active ? 'danger' : 'secondary'} onclick={archiveProject}>{selectedProject?.active ? 'Archive project' : 'Restore project'}</Button></p>
		{/if}
		<h3 class="mt-4 mb-2 font-semibold">New project</h3>
		<form onsubmit={createProject}>
			<fieldset class="fieldset rounded-box border border-base-300 bg-base-100 p-4 flex flex-wrap items-end gap-4" disabled={busy}>
			<legend class="fieldset-legend">New project details</legend>
			<Field label="Name" id="new-project-name">
				<input class="input" id="new-project-name" bind:value={newProjectName} required />
			</Field>
			<Field label="Client" id="new-project-client">
				<Select id="new-project-client" bind:value={newProjectClient} options={clientOptions} />
			</Field>
			<Field label="Hourly rate ($)" id="new-project-rate">
				<input class="input"
					id="new-project-rate"
					type="number"
					min="0"
					step="0.01"
					bind:value={newProjectRate}
					required
				/>
			</Field>
			<Button type="submit" disabled={busy || newProjectClient === ''}>Create project</Button>
			</fieldset>
		</form>
	</section>
{/if}
{/if}

{#if error}
	<Toast message={error} tone="danger" onDismiss={() => (error = '')} />
{/if}

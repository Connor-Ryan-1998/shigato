<script lang="ts">
	import { onMount } from 'svelte';
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
	type Project = { id: number; name: string; client_contact_id: number; hourly_rate: number };
	type Task = { id: number; name: string; hourly_rate?: number; active: boolean };
	type Entry = {
		id: number;
		task_id: number;
		work_date: string;
		minutes: number;
		note: string;
		invoice_line_id?: number;
	};

	const org = $derived(page.params.org);
	const base = $derived(`/api/v1/orgs/${page.params.org}`);
	const isAdmin = $derived(session.currentOrg?.perms.includes('timesheets_admin') ?? false);

	let projects = $state<Project[]>([]);
	let projectId = $state('');
	let tasks = $state<Task[]>([]);
	let entries = $state<Entry[]>([]);
	let totalMinutes = $state(0);
	let contacts = $state<Contact[]>([]);
	let error = $state('');

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

	const projectOptions = $derived(projects.map((p) => ({ value: String(p.id), label: p.name })));
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
		projects = await api<Project[]>('GET', `${base}/projects?active=true`);
		if (!projects.some((p) => String(p.id) === projectId)) {
			projectId = projects[0] ? String(projects[0].id) : '';
		}
	}

	let seq = 0;
	async function loadProject(id: string) {
		const s = ++seq;
		if (id === '') {
			tasks = [];
			entries = [];
			totalMinutes = 0;
			return;
		}
		try {
			const [t, e] = await Promise.all([
				api<Task[]>('GET', `${base}/projects/${id}/tasks`),
				api<{ entries: Entry[]; total_minutes: number }>(
					'GET',
					`${base}/time-entries?project_id=${id}`
				)
			]);
			if (s !== seq) return;
			tasks = t;
			entries = e.entries;
			totalMinutes = e.total_minutes;
			if (!t.some((x) => x.active && String(x.id) === taskId)) {
				const first = t.find((x) => x.active);
				taskId = first ? String(first.id) : '';
			}
		} catch (e) {
			if (s === seq) fail(e);
		}
	}

	onMount(async () => {
		try {
			await loadProjects();
			if (isAdmin) {
				contacts = await api<Contact[]>('GET', `${base}/contacts`);
				newProjectClient = clientOptions[0]?.value ?? '';
			}
		} catch (e) {
			fail(e);
		}
	});

	$effect(() => {
		loadProject(projectId);
	});

	async function act(fn: () => Promise<unknown>) {
		error = '';
		try {
			await fn();
		} catch (e) {
			fail(e);
		}
	}

	function logTime(e: SubmitEvent) {
		e.preventDefault();
		act(async () => {
			await api('POST', `${base}/time-entries`, {
				task_id: Number(taskId),
				work_date: workDate,
				minutes: Number(minutes),
				note: note || undefined
			});
			note = '';
			await loadProject(projectId);
		});
	}

	function deleteEntry(id: number) {
		act(async () => {
			await api('DELETE', `${base}/time-entries/${id}`);
			await loadProject(projectId);
		});
	}

	function createProject(e: SubmitEvent) {
		e.preventDefault();
		act(async () => {
			const p = await api<Project>('POST', `${base}/projects`, {
				name: newProjectName,
				client_contact_id: Number(newProjectClient),
				hourly_rate: Math.round(Number(newProjectRate) * 100)
			});
			newProjectName = '';
			newProjectRate = '';
			await loadProjects();
			projectId = String(p.id);
		});
	}

	function createTask(e: SubmitEvent) {
		e.preventDefault();
		act(async () => {
			await api('POST', `${base}/projects/${projectId}/tasks`, { name: newTaskName });
			newTaskName = '';
			await loadProject(projectId);
		});
	}

	function setTaskActive(t: Task, active: boolean) {
		act(async () => {
			await api('PUT', `${base}/tasks/${t.id}`, { active });
			await loadProject(projectId);
		});
	}

	function archiveProject() {
		act(async () => {
			await api('PUT', `${base}/projects/${projectId}`, { active: false });
			await loadProjects();
		});
	}

	function hours(m: number): string {
		return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, '0')}m`;
	}
</script>

<PageHeader title="Time" />

<Field label="Project" id="time-project">
	<Select id="time-project" bind:value={projectId} options={projectOptions} />
</Field>
{#if projects.length === 0}
	<p>No active projects.{isAdmin ? ' Create one below.' : ''}</p>
{/if}

{#if projectId !== ''}
	<section aria-labelledby="log-heading">
		<h2 id="log-heading">Log time</h2>
		{#if taskOptions.length === 0}
			<p>This project has no active tasks.</p>
		{:else}
			<form class="row" onsubmit={logTime}>
				<Field label="Task" id="log-task">
					<Select id="log-task" bind:value={taskId} options={taskOptions} />
				</Field>
				<Field label="Date" id="log-date">
					<input id="log-date" type="date" bind:value={workDate} required />
				</Field>
				<Field label="Minutes" id="log-minutes">
					<input id="log-minutes" type="number" min="1" step="1" bind:value={minutes} required />
				</Field>
				<Field label="Note" id="log-note"><input id="log-note" bind:value={note} /></Field>
				<Button type="submit">Log time</Button>
			</form>
		{/if}
	</section>

	<section aria-labelledby="entries-heading">
		<h2 id="entries-heading">Entries ({hours(totalMinutes)})</h2>
		{#if entries.length === 0}
			<p>No time logged on this project.</p>
		{:else}
			<table>
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
								{:else}
									<Button variant="danger" onclick={() => deleteEntry(e.id)}>Delete</Button>
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{/if}
	</section>
{/if}

{#if isAdmin}
	<section aria-labelledby="admin-heading">
		<h2 id="admin-heading">Projects and tasks</h2>
		{#if projectId !== ''}
			<h3>Tasks</h3>
			<ul>
				{#each tasks as t (t.id)}
					<li>
						{t.name}{t.hourly_rate !== undefined ? ` (${money(t.hourly_rate)}/h)` : ''}
						<Button variant="secondary" onclick={() => setTaskActive(t, !t.active)}>
							{t.active ? 'Archive' : 'Restore'}
						</Button>
					</li>
				{/each}
			</ul>
			<form class="row" onsubmit={createTask}>
				<Field label="New task" id="new-task">
					<input id="new-task" bind:value={newTaskName} required />
				</Field>
				<Button type="submit">Add task</Button>
			</form>
			<p><Button variant="danger" onclick={archiveProject}>Archive project</Button></p>
		{/if}
		<h3>New project</h3>
		<form class="row" onsubmit={createProject}>
			<Field label="Name" id="new-project-name">
				<input id="new-project-name" bind:value={newProjectName} required />
			</Field>
			<Field label="Client" id="new-project-client">
				<Select id="new-project-client" bind:value={newProjectClient} options={clientOptions} />
			</Field>
			<Field label="Hourly rate ($)" id="new-project-rate">
				<input
					id="new-project-rate"
					type="number"
					min="0"
					step="0.01"
					bind:value={newProjectRate}
					required
				/>
			</Field>
			<Button type="submit" disabled={newProjectClient === ''}>Create project</Button>
		</form>
	</section>
{/if}

{#if error}
	<Toast message={error} tone="danger" onDismiss={() => (error = '')} />
{/if}

<style>
	section {
		margin-top: var(--space-8, 2rem);
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-4, 1rem);
		align-items: flex-end;
	}
	table {
		width: 100%;
		border-collapse: collapse;
	}
	th,
	td {
		text-align: left;
		padding: var(--space-2, 0.5rem);
		border-bottom: 1px solid var(--border, #ddd);
	}
</style>

<script lang="ts">
	// LogTimeDialog creates a time entry, or edits one when `entry` is set.
	// Used by the project detail page ("Log Time") and Time-Logging ("New
	// Log Entry" and row edits).
	import { untrack } from 'svelte';
	import { api, ApiError } from '../../api';
	import { todayLocal } from '../../format';
	import Dialog from '../../components/Dialog.svelte';
	import Field from '../../components/Field.svelte';
	import Button from '../../components/Button.svelte';
	import { hhmm, parseDuration, type Entry, type Project, type Task } from './types';

	let {
		open = $bindable(false),
		org,
		projects,
		entry = null,
		projectId = null,
		onsaved
	}: {
		open?: boolean;
		org: string;
		// Projects to offer; closed ones are filtered out (except an edited
		// entry's own project).
		projects: Project[];
		entry?: Entry | null;
		// Preselects (and locks) the project, e.g. on its detail page.
		projectId?: number | null;
		onsaved: () => void;
	} = $props();

	let selectedProject = $state('');
	let taskId = $state('');
	let workDate = $state('');
	let duration = $state('');
	let note = $state('');
	let tasks = $state<Task[]>([]);
	let error = $state('');
	let busy = $state(false);
	let taskSeq = 0;

	const projectOptions = $derived(
		projects.filter((p) => p.active || p.id === entry?.project_id)
	);
	const taskOptions = $derived(tasks.filter((t) => t.active || t.id === entry?.task_id));

	// Re-seed every time the dialog opens (only `open` is tracked, so a
	// projects refresh mid-edit doesn't wipe the form).
	$effect(() => {
		if (open) untrack(seed);
	});
	function seed() {
		error = '';
		busy = false;
		selectedProject = String(entry?.project_id ?? projectId ?? projectOptions[0]?.id ?? '');
		taskId = entry ? String(entry.task_id) : '';
		workDate = entry?.work_date ?? todayLocal();
		duration = entry ? hhmm(entry.minutes) : '';
		note = entry?.note ?? '';
	}

	$effect(() => {
		const id = selectedProject;
		if (!open || id === '') {
			tasks = [];
			return;
		}
		const seq = ++taskSeq;
		api<Task[]>('GET', `/api/v1/orgs/${org}/projects/${id}/tasks`)
			.then((t) => {
				if (seq !== taskSeq) return;
				tasks = t;
				if (!taskOptions.some((x) => String(x.id) === taskId)) {
					taskId = taskOptions[0] ? String(taskOptions[0].id) : '';
				}
			})
			.catch((e) => {
				if (seq === taskSeq) error = e instanceof ApiError ? e.message : 'Something went wrong.';
			});
	});

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		const minutes = parseDuration(duration);
		if (minutes === null) {
			error = 'Time must be H:MM or minutes, between 0:01 and 24:00.';
			return;
		}
		if (taskId === '') {
			error = 'Choose a task.';
			return;
		}
		busy = true;
		error = '';
		const body = { task_id: Number(taskId), work_date: workDate, minutes, note };
		try {
			if (entry) await api('PUT', `/api/v1/orgs/${org}/time-entries/${entry.id}`, body);
			else await api('POST', `/api/v1/orgs/${org}/time-entries`, body);
			open = false;
			onsaved();
		} catch (err) {
			error = err instanceof ApiError ? err.message : 'Something went wrong.';
		} finally {
			busy = false;
		}
	}
</script>

<Dialog bind:open title={entry ? 'Edit time entry' : 'Log time'}>
	{#if open}
		<form class="flex flex-col gap-3" onsubmit={submit}>
			<Field label="Project" id="log-project">
				<select
					class="select w-full"
					id="log-project"
					bind:value={selectedProject}
					disabled={busy || projectId !== null}
					required
				>
					{#each projectOptions as p (p.id)}
						<option value={String(p.id)}>{p.name}</option>
					{/each}
				</select>
			</Field>
			<Field label="Task" id="log-task">
				<select class="select w-full" id="log-task" bind:value={taskId} disabled={busy} required>
					{#each taskOptions as t (t.id)}
						<option value={String(t.id)}>{t.name}</option>
					{/each}
				</select>
			</Field>
			<div class="grid grid-cols-2 gap-3">
				<Field label="Date" id="log-date">
					<input class="input w-full" id="log-date" type="date" bind:value={workDate} disabled={busy} required />
				</Field>
				<Field label="Time (H:MM)" id="log-duration">
					<input
						class="input w-full"
						id="log-duration"
						placeholder="1:30"
						bind:value={duration}
						disabled={busy}
						required
					/>
				</Field>
			</div>
			<Field label="Note" id="log-note">
				<textarea class="textarea w-full" id="log-note" maxlength="1000" bind:value={note} disabled={busy}
				></textarea>
			</Field>
			{#if error}
				<p class="alert alert-error alert-soft" role="alert">{error}</p>
			{/if}
			<div class="modal-action">
				<Button variant="secondary" disabled={busy} onclick={() => (open = false)}>Cancel</Button>
				<Button type="submit" disabled={busy}>{entry ? 'Save entry' : 'Log time'}</Button>
			</div>
		</form>
	{/if}
</Dialog>

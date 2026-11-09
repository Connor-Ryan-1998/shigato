<script lang="ts">
	// ProjectDialog creates a project, or edits one when `project` is set
	// (a full PUT: every field is sent).
	import { untrack } from 'svelte';
	import { api, ApiError } from '../../api';
	import { parseCents } from '../../format';
	import Dialog from '../../components/Dialog.svelte';
	import Field from '../../components/Field.svelte';
	import Button from '../../components/Button.svelte';
	import {
		BILLING_LABELS,
		STATUS_LABELS,
		type BillingMethod,
		type Project,
		type ProjectStatus
	} from './types';

	type Contact = { id: number; name: string; kind: string };

	let {
		open = $bindable(false),
		org,
		contacts,
		project = null,
		allowed = true,
		onsaved
	}: {
		open?: boolean;
		org: string;
		contacts: Contact[];
		project?: Project | null;
		allowed?: boolean;
		onsaved: (p: Project) => void;
	} = $props();

	let name = $state('');
	let client = $state('');
	let status = $state<ProjectStatus>('in_progress');
	let billing = $state<BillingMethod>('tm');
	let rate = $state('');
	let gst = $state('GST');
	let start = $state('');
	let end = $state('');
	let description = $state('');
	let error = $state('');
	let busy = $state(false);
	let actionSeq = 0;
	$effect(() => { org; open; allowed; return () => { actionSeq++; }; });

	const clients = $derived(contacts.filter((c) => c.kind === 'client'));

	$effect(() => {
		if (open) untrack(seed);
	});
	function seed() {
		error = '';
		busy = false;
		name = project?.name ?? '';
		client = String(project?.client_contact_id ?? clients[0]?.id ?? '');
		status = project?.status ?? 'in_progress';
		billing = project?.billing_method ?? 'tm';
		rate = project ? (project.hourly_rate / 100).toFixed(2) : '';
		gst = project?.gst_code ?? 'GST';
		start = project?.start_date ?? '';
		end = project?.end_date ?? '';
		description = project?.description ?? '';
	}

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (busy || !open || !allowed) return;
		const slug = org;
		const seq = actionSeq;
		const cents = parseCents(rate);
		if (cents === null || cents <= 0) {
			error = 'Base rate must be greater than zero.';
			return;
		}
		if (start && end && end < start) {
			error = 'End date must not be before start date.';
			return;
		}
		const body = {
			name,
			client_contact_id: Number(client),
			hourly_rate: cents,
			gst_code: gst,
			status,
			description,
			start_date: start || null,
			end_date: end || null,
			billing_method: billing
		};
		busy = true;
		error = '';
		try {
			const saved = project
				? await api<Project>('PUT', `/api/v1/orgs/${org}/projects/${project.id}`, body)
				: await api<Project>('POST', `/api/v1/orgs/${org}/projects`, body);
			if (seq !== actionSeq || slug !== org || !open || !allowed) return;
			open = false;
			onsaved(saved);
		} catch (err) {
			if (seq === actionSeq && slug === org && open && allowed) error = err instanceof ApiError ? err.message : 'Something went wrong.';
		} finally {
			if (seq === actionSeq) busy = false;
		}
	}
</script>

<Dialog bind:open title={project ? 'Edit project' : 'New project'} busy={busy || !allowed}>
	{#snippet children({ close })}
	{#if open}
		<form class="flex flex-col gap-3" onsubmit={submit}>
			<Field label="Project name" id="project-name">
				<input class="input w-full" id="project-name" bind:value={name} disabled={busy} required />
			</Field>
			<Field label="Customer" id="project-customer">
				<select class="select w-full" id="project-customer" bind:value={client} disabled={busy} required>
					{#each clients as c (c.id)}
						<option value={String(c.id)}>{c.name}</option>
					{/each}
				</select>
			</Field>
			<div class="grid grid-cols-2 gap-3">
				<Field label="Status" id="project-status">
					<select class="select w-full" id="project-status" bind:value={status} disabled={busy}>
						{#each Object.entries(STATUS_LABELS) as [value, label] (value)}
							<option {value}>{label}</option>
						{/each}
					</select>
				</Field>
				<Field label="Billing method" id="project-billing">
					<select class="select w-full" id="project-billing" bind:value={billing} disabled={busy}>
						{#each Object.entries(BILLING_LABELS) as [value, label] (value)}
							<option {value}>{label}</option>
						{/each}
					</select>
				</Field>
				<Field label="Base rate ($/h)" id="project-rate">
					<input class="input w-full" id="project-rate" inputmode="decimal" bind:value={rate} disabled={busy} required />
				</Field>
				<Field label="GST" id="project-gst">
					<select class="select w-full" id="project-gst" bind:value={gst} disabled={busy}>
						<option value="GST">GST</option>
						<option value="FRE">GST free</option>
					</select>
				</Field>
				<Field label="Start date" id="project-start">
					<input class="input w-full" id="project-start" type="date" bind:value={start} disabled={busy} />
				</Field>
				<Field label="End date" id="project-end">
					<input class="input w-full" id="project-end" type="date" bind:value={end} disabled={busy} />
				</Field>
			</div>
			<Field label="Description" id="project-description">
				<textarea class="textarea w-full" id="project-description" maxlength="2000" bind:value={description} disabled={busy}
				></textarea>
			</Field>
			{#if error}
				<p class="alert alert-error alert-soft" role="alert">{error}</p>
			{/if}
			<div class="modal-action">
				<Button variant="secondary" disabled={busy} onclick={close}>Cancel</Button>
				<Button type="submit" disabled={busy || client === ''}>{project ? 'Save project' : 'Create project'}</Button>
			</div>
		</form>
	{/if}
	{/snippet}
</Dialog>

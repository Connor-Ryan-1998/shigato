<script lang="ts">
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../api';
	import { minutes, money } from '../../format';
	import Dialog from '../../components/Dialog.svelte';
	import Field from '../../components/Field.svelte';
	import Select from '../../components/Select.svelte';
	import Button from '../../components/Button.svelte';
	import {
		filterProjects,
		projectState,
		selectionPayload,
		selectionTotals,
		toggleProject,
		type PreviewProject
	} from './billingSelection';

	// The billing run: pick a client and period, review the unbilled time by
	// project and task, untick what shouldn't be billed yet, and create one
	// invoice or one per project. The server re-checks the reviewed minutes
	// and amounts and refuses (409) if anything changed since the preview.
	let {
		open = $bindable(false),
		org,
		defaultFrom,
		defaultTo,
		oncomplete
	}: {
		open?: boolean;
		org: string;
		defaultFrom: string;
		defaultTo: string;
		// Called once invoices exist, so the opener can refresh its list.
		oncomplete?: () => void;
	} = $props();

	type Contact = { id: number; name: string; kind: string };
	type Preview = { projects: PreviewProject[] };
	type RunInvoice = { invoice: { id: number; number: number | null; status: string }; issue_error?: string };
	type RunResult = { invoices: RunInvoice[] };

	let contacts = $state<Contact[]>([]);
	let contactsError = $state('');
	let clientId = $state('');
	let from = $state('');
	let to = $state('');
	let dueInDays = $state('14');
	let issue = $state(false);
	let split = $state<'combined' | 'project'>('combined');
	let query = $state('');
	let projects = $state<PreviewProject[] | null>(null);
	let previewError = $state('');
	let selected = $state.raw<Set<number>>(new Set());
	let running = $state(false);
	let runError = $state('');
	let stale = $state(false);
	let results = $state<RunInvoice[] | null>(null);
	let previewSeq = 0;
	let openSeq = 0;

	const clientOptions = $derived(contacts.map((c) => ({ value: String(c.id), label: c.name })));
	const visible = $derived(projects ? filterProjects(projects, query) : []);
	const totals = $derived(projects ? selectionTotals(projects, selected) : { minutes: 0, amount: 0, gst_amount: 0 });
	const rangeValid = $derived(from !== '' && to !== '' && from <= to);
	const canSubmit = $derived(!running && projects !== null && selected.size > 0 && results === null);

	function message(e: unknown, fallback: string): string {
		return e instanceof ApiError ? e.message : fallback;
	}

	// Reset and load clients each time the dialog opens.
	$effect(() => {
		if (!open) return;
		const token = ++openSeq;
		contacts = [];
		contactsError = '';
		clientId = '';
		from = defaultFrom;
		to = defaultTo;
		dueInDays = '14';
		issue = false;
		split = 'combined';
		query = '';
		projects = null;
		selected = new Set();
		runError = '';
		stale = false;
		results = null;
		api<Contact[]>('GET', `/api/v1/orgs/${org}/contacts`)
			.then((all) => {
				if (token !== openSeq) return;
				contacts = all.filter((c) => c.kind === 'client');
				if (contacts[0]) clientId = String(contacts[0].id);
			})
			.catch((e) => {
				if (token === openSeq) contactsError = message(e, 'Could not load clients.');
			});
	});

	// Any change of client or period discards the preview and loads a new one.
	$effect(() => {
		const client = clientId;
		const range = rangeValid;
		const f = from;
		const t = to;
		if (!open || client === '' || !range) {
			previewSeq++;
			projects = null;
			return;
		}
		loadPreview(client, f, t, null);
	});

	async function loadPreview(client: string, f: string, t: string, keep: Set<number> | null) {
		const token = ++previewSeq;
		projects = null;
		previewError = '';
		stale = false;
		runError = '';
		const params = new URLSearchParams({ client_contact_id: client, from: f, to: t });
		try {
			const res = await api<Preview>('GET', `/api/v1/orgs/${org}/billing-runs/preview?${params}`);
			if (token !== previewSeq) return;
			const ids = res.projects.flatMap((p) => p.tasks.map((tk) => tk.task_id));
			selected = new Set(keep ? ids.filter((id) => keep.has(id)) : ids);
			projects = res.projects;
		} catch (e) {
			if (token === previewSeq) previewError = message(e, 'Could not load unbilled time.');
		}
	}

	function refresh() {
		if (running || clientId === '' || !rangeValid) return;
		loadPreview(clientId, from, to, selected);
	}

	function toggleTask(id: number) {
		const next = new Set(selected);
		if (next.has(id)) next.delete(id);
		else next.add(id);
		selected = next;
	}

	function toggle(projectId: number) {
		const full = projects?.find((p) => p.project_id === projectId);
		if (full) selected = toggleProject(full, selected);
	}

	function indeterminate(node: HTMLInputElement, value: boolean) {
		node.indeterminate = value;
		return { update: (v: boolean) => (node.indeterminate = v) };
	}

	async function run(event: SubmitEvent) {
		event.preventDefault();
		if (!canSubmit || !projects) return;
		const days = Number(dueInDays);
		if (!Number.isInteger(days) || days < 0 || days > 90) {
			runError = 'Due in days must be between 0 and 90.';
			return;
		}
		const token = openSeq;
		running = true;
		runError = '';
		stale = false;
		try {
			const res = await api<RunResult>('POST', `/api/v1/orgs/${org}/billing-runs`, {
				client_contact_id: Number(clientId),
				from,
				to,
				due_in_days: days,
				issue,
				split,
				tasks: selectionPayload(projects, selected)
			});
			if (token !== openSeq) return;
			running = false;
			oncomplete?.();
			const only = res.invoices.length === 1 ? res.invoices[0] : null;
			if (only && !only.issue_error) {
				open = false;
				await goto(`/${org}/invoices/${only.invoice.id}`);
				return;
			}
			results = res.invoices;
		} catch (e) {
			if (token !== openSeq) return;
			runError = message(e, 'Could not run billing.');
			stale = e instanceof ApiError && e.status === 409;
		} finally {
			if (token === openSeq) running = false;
		}
	}

	function invoiceLabel(inv: RunInvoice['invoice']): string {
		return inv.number !== null ? `Invoice #${inv.number}` : 'Draft invoice';
	}
</script>

<Dialog bind:open title="Billing run" busy={running}>
	{#snippet children({ close })}
		{#if results}
			<p class="mb-3">The billing run created these invoices. Review any that could not be issued.</p>
			<ul class="mb-4 grid gap-2">
				{#each results as r (r.invoice.id)}
					<li>
						<a class="link link-primary" href={`/${org}/invoices/${r.invoice.id}`} onclick={() => (open = false)}>{invoiceLabel(r.invoice)}</a>
						<span class="text-base-content/70"> · {r.invoice.status}</span>
						{#if r.issue_error}<p class="text-error text-sm">Not issued: {r.issue_error}</p>{/if}
					</li>
				{/each}
			</ul>
			<div class="flex justify-end"><Button onclick={() => (open = false)}>Close</Button></div>
		{:else}
			<form onsubmit={run} class="grid gap-4">
				<fieldset disabled={running} class="grid gap-3">
					{#if contactsError}<p role="alert" class="alert alert-error alert-soft">{contactsError}</p>{/if}
					<Field label="Client" id="run-client">
						<Select id="run-client" bind:value={clientId} options={clientOptions} />
					</Field>
					<div class="grid grid-cols-2 gap-3">
						<Field label="From" id="run-from"><input class="input w-full" id="run-from" type="date" bind:value={from} required /></Field>
						<Field label="To" id="run-to"><input class="input w-full" id="run-to" type="date" bind:value={to} required /></Field>
					</div>
					{#if from !== '' && to !== '' && !rangeValid}<p class="text-error text-sm">From must not be after To.</p>{/if}

					{#if previewError}
						<p role="alert" class="alert alert-error alert-soft">{previewError}</p>
					{:else if clientId !== '' && rangeValid && projects === null}
						<p role="status">Loading unbilled time…</p>
					{:else if projects && projects.length === 0}
						<p role="status">No unbilled time for this client in this period.</p>
					{:else if projects}
						<Field label="Search projects and tasks" id="run-search">
							<input class="input w-full" id="run-search" type="search" bind:value={query} />
						</Field>
						{#if query.trim() !== ''}
							<p class="text-sm text-base-content/70">Project boxes select or clear all of a project's tasks, including ones hidden by search.</p>
						{/if}
						<div class="max-h-64 overflow-y-auto rounded-box border border-base-300 p-2">
							{#each visible as p (p.project_id)}
								{@const full = projects.find((x) => x.project_id === p.project_id) ?? p}
								{@const state = projectState(full, selected)}
								<label class="label w-full font-semibold">
									<input class="checkbox checkbox-sm" type="checkbox" checked={state === 'all'} use:indeterminate={state === 'some'} onchange={() => toggle(p.project_id)} />
									{p.project_name}
								</label>
								<ul class="mb-2 ml-6">
									{#each p.tasks as t (t.task_id)}
										<li>
											<label class="label w-full justify-between">
												<span><input class="checkbox checkbox-sm" type="checkbox" checked={selected.has(t.task_id)} onchange={() => toggleTask(t.task_id)} /> {t.task_name}</span>
												<span class="tabular-nums">{minutes(t.minutes)} · {money(t.amount)}</span>
											</label>
										</li>
									{/each}
								</ul>
							{:else}
								<p class="text-base-content/70">No projects or tasks match.</p>
							{/each}
						</div>
						<p class="tabular-nums" aria-live="polite">
							Selected: {minutes(totals.minutes)} · {money(totals.amount)}{totals.gst_amount > 0 ? ` + ${money(totals.gst_amount)} GST` : ''}
						</p>
					{/if}

					<div class="grid gap-1" role="radiogroup" aria-label="Invoices">
						<label class="label"><input class="radio radio-sm" type="radio" name="run-split" value="combined" bind:group={split} /> One combined invoice</label>
						<label class="label"><input class="radio radio-sm" type="radio" name="run-split" value="project" bind:group={split} /> One invoice per project</label>
					</div>
					<Field label="Due in days" id="run-due">
						<input class="input w-full" id="run-due" type="number" min="0" max="90" step="1" bind:value={dueInDays} required />
					</Field>
					<label class="label"><input class="checkbox" type="checkbox" bind:checked={issue} /> Issue immediately</label>
				</fieldset>
				{#if runError}
					<p role="alert" class="alert alert-error alert-soft">{runError}</p>
				{/if}
				<div class="flex flex-wrap justify-end gap-3">
					{#if stale}<Button variant="secondary" disabled={running} onclick={refresh}>Refresh preview</Button>{/if}
					<Button variant="secondary" disabled={running} onclick={close}>Cancel</Button>
					<Button type="submit" disabled={!canSubmit}>{running ? 'Creating…' : 'Create invoices'}</Button>
				</div>
			</form>
		{/if}
	{/snippet}
</Dialog>

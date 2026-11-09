<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError } from '../../../../lib/api';
	import { money, todayLocal } from '../../../../lib/format';
	import { session } from '../../../../lib/session.svelte';
	import PageHeader from '../../../../lib/components/PageHeader.svelte';
	import Field from '../../../../lib/components/Field.svelte';
	import Button from '../../../../lib/components/Button.svelte';
	import Toast from '../../../../lib/components/Toast.svelte';
	import Dialog from '../../../../lib/components/Dialog.svelte';
	import LoadState from '../../../../lib/components/LoadState.svelte';

	type Lease = {
		id: number;
		start_date: string;
		end_date?: string;
		termination_date?: string;
		frequency: string;
		bond_amount: number;
		bond_status: string;
		rta_lodgement_ref?: string;
		warnings?: string[];
		rent_changes: { id: number; effective_date: string; amount: number }[];
		charges: { id: number; description: string; amount: number; start_date: string; end_date?: string }[];
	};
	type Ledger = {
		periods: {
			start: string;
			end: string;
			amount_due: number;
			invoice_id?: number;
			invoice_status?: string;
			paid: number;
		}[];
		opening_arrears: number;
		arrears: number;
		invoiced_not_due: number;
		unissued: number;
	};
	type AffectedInvoice = {
		invoice_id: number;
		invoice_number?: number;
		status: string;
		start: string;
		end: string;
	};

	const org = $derived(page.params.org);
	const url = $derived(`/api/v1/orgs/${page.params.org}/leases/${page.params.id}`);
	const membership = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(membership?.perms.includes('property_read') ?? false);
	const canWrite = $derived(membership?.perms.includes('property_write') ?? false);

	let lease = $state<Lease | null>(null);
	let ledger = $state<Ledger | null>(null);
	let error = $state('');
	let notice = $state('');
	let affectedInvoices = $state<AffectedInvoice[]>([]);
	let loadedURL = $state('');
	let busy = $state(false);
	let loadSeq = 0;
	let actionSeq = 0;
	let actionOpen = $state(false);
	let action = $state('Change rent');
	function openAction(title: string) {
		if (busy || !canWrite || loadedURL !== url) return;
		action = title; error = ''; rcDate = today; rcAmount = ''; chDesc = ''; chAmount = ''; chStart = today; chEnd = ''; termDate = today; bondRef = ''; bondDate = today; actionOpen = true;
	}

	const today = todayLocal();
	let rcDate = $state(today);
	let rcAmount = $state('');
	let chDesc = $state('');
	let chAmount = $state('');
	let chStart = $state(today);
	let chEnd = $state('');
	let termDate = $state(today);
	let bondRef = $state('');
	let bondDate = $state(today);

	const cents = (d: string) => Math.round(Number(d || 0) * 100);

	function fail(e: unknown) {
		error = e instanceof ApiError ? e.message : 'Something went wrong.';
	}

	async function load(target: string) {
		const seq = ++loadSeq;
		try {
			const [nextLease, nextLedger] = await Promise.all([
				api<Lease>('GET', target),
				api<Ledger>('GET', `${target}/ledger`)
			]);
			if (seq !== loadSeq || target !== url || !canRead) return;
			lease = nextLease;
			ledger = nextLedger;
			loadedURL = target;
		} catch (e) {
			if (seq === loadSeq && target === url && canRead) fail(e);
		}
	}

	$effect(() => {
		const target = url;
		const allowed = canRead;
		lease = null;
		ledger = null;
		loadedURL = '';
		affectedInvoices = [];
		notice = '';
		error = '';
		busy = false;
		actionOpen = false;
		rcDate = today;
		rcAmount = '';
		chDesc = '';
		chAmount = '';
		chStart = today;
		chEnd = '';
		termDate = today;
		bondRef = '';
		bondDate = today;
		if (allowed) load(target);
		return () => { loadSeq++; actionSeq++; };
	});

	async function act(e: SubmitEvent | null, path: string, body: unknown, done = '') {
		e?.preventDefault();
		if (!canWrite || loadedURL !== url || busy) return;
		const target = url;
		const seq = ++actionSeq;
		busy = true;
		error = '';
		notice = '';
		affectedInvoices = [];
		try {
			const res = await api<{ warnings?: string[]; affected_invoices?: AffectedInvoice[] }>('POST', `${target}/${path}`, body);
			if (seq !== actionSeq || target !== url || !canWrite) return;
			notice = [done, ...(res?.warnings ?? [])].filter(Boolean).join(' ');
			affectedInvoices = res?.affected_invoices ?? [];
			actionOpen = false;
			await load(target);
		} catch (err) {
			if (seq === actionSeq && target === url && canWrite) fail(err);
		} finally {
			if (seq === actionSeq) busy = false;
		}
	}
</script>

<PageHeader title={`Lease ${page.params.id}`} back={{ href: `/${org}/properties`, label: 'Back to properties' }} />

{#if !canRead}
	<p role="alert">You do not have permission to view this lease.</p>
{:else if loadedURL !== url}
	<LoadState loading={true} {error} loadingLabel="Loading lease…" onretry={() => { error = ''; load(url); }}><span></span></LoadState>
{/if}

{#if canRead && loadedURL === url && affectedInvoices.length > 0}
	<section role="status" aria-labelledby="invoice-warning-title" class="alert alert-warning alert-soft mb-4 flex-col items-start">
		<h2 id="invoice-warning-title" class="font-semibold">Invoice review required</h2>
		<p>Saved change overlaps existing invoice periods. Existing invoices were not recalculated; review them before making any manual correction.</p>
		<ul class="list-disc pl-5">
			{#each affectedInvoices as invoice (`${invoice.invoice_id}:${invoice.start}`)}
				<li>
					<a class="link" href={`/${org}/invoices/${invoice.invoice_id}`}>
						{invoice.status === 'draft' ? `Draft invoice ${invoice.invoice_id}` : invoice.invoice_number != null ? `Invoice #${invoice.invoice_number}` : `Invoice ${invoice.invoice_id}`}
					</a>
					· {invoice.status} · <span>{invoice.start} to {invoice.end}</span>
				</li>
			{/each}
		</ul>
	</section>
{/if}

{#if canRead && loadedURL === url && lease}
	<dl class="mb-4 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-2 [&_dt]:font-semibold [&_dt]:text-base-content/70">
		<dt>Term</dt>
		<dd>
			{lease.start_date} to {lease.termination_date ?? lease.end_date ?? 'ongoing'}{lease.termination_date
				? ' (terminated)'
				: ''}
		</dd>
		<dt>Frequency</dt>
		<dd>{lease.frequency}</dd>
		<dt>Bond</dt>
		<dd>
			{money(lease.bond_amount)} · {lease.bond_status}{lease.rta_lodgement_ref
				? ` (RTA ${lease.rta_lodgement_ref})`
				: ''}
		</dd>
	</dl>
	{#each lease.warnings ?? [] as w (w)}
		<p class="alert alert-warning alert-soft mb-2" role="status">{w}</p>
	{/each}

	<h2 class="mt-6 mb-3 text-lg font-semibold">Rent</h2>
	<ul class="list-disc pl-5">
		{#each lease.rent_changes as rc (rc.id)}
			<li>{money(rc.amount)} from {rc.effective_date}</li>
		{/each}
	</ul>
	{#if lease.charges.length > 0}
		<h2 class="mt-6 mb-3 text-lg font-semibold">Charges</h2>
		<ul class="list-disc pl-5">
			{#each lease.charges as c (c.id)}
				<li>{c.description}: {money(c.amount)} from {c.start_date}{c.end_date ? ` to ${c.end_date}` : ''}</li>
			{/each}
		</ul>
	{/if}
{/if}

{#if canRead && loadedURL === url && ledger}
	<h2 class="mt-6 mb-3 text-lg font-semibold">Ledger</h2>
	<p class="mb-3 text-base-content/70">
		Arrears {money(ledger.arrears)} · Invoiced, not due {money(ledger.invoiced_not_due)} · Unissued {money(
			ledger.unissued
		)}{ledger.opening_arrears ? ` · Opening arrears ${money(ledger.opening_arrears)}` : ''}
	</p>
	<div class="overflow-x-auto">
	<table class="table">
		<thead>
			<tr><th>Period</th><th>Due</th><th>Paid</th><th>Invoice</th></tr>
		</thead>
		<tbody>
			{#each ledger.periods as p (p.start)}
				<tr>
					<td>{p.start} to {p.end}</td>
					<td>{money(p.amount_due)}</td>
					<td>{money(p.paid)}</td>
					<td>
						{#if p.invoice_id}
							<a class="link link-primary" href={`/${org}/invoices/${p.invoice_id}`}>{p.invoice_status}</a>
						{:else}
							Not invoiced
						{/if}
					</td>
				</tr>
			{:else}
				<tr><td colspan="4">No periods yet.</td></tr>
			{/each}
		</tbody>
	</table>
	</div>
{/if}

{#if lease && loadedURL === url && canWrite && !lease.termination_date}
	<h2 class="mt-6 mb-3 text-lg font-semibold">Actions</h2>
	<div class="mb-4 flex flex-wrap gap-2">
		{#each ['Change rent', 'Add charge', ...(lease.bond_amount > 0 ? ['Record lodgement'] : []), 'Terminate lease'] as title}
			<Button variant={title === 'Terminate lease' ? 'danger' : 'secondary'} disabled={busy} onclick={() => openAction(title)}>{title}</Button>
		{/each}
		{#if lease.bond_amount > 0}<Button variant="secondary" disabled={busy} onclick={() => act(null, 'bond-invoice', {}, 'Bond invoice created.')}>Create bond invoice</Button>{/if}
	</div>
	<Dialog bind:open={actionOpen} title={action} {busy}>
	{#snippet children({ close })}
	{#if actionOpen}
	{#if error}<p role="alert" class="alert alert-error alert-soft mb-4">{error}</p>{/if}
	{#if action === 'Change rent'}
	<form
		onsubmit={(e) =>
			act(e, 'rent-changes', { effective_date: rcDate, amount: cents(rcAmount) }, 'Rent change saved.')}
	>
		<fieldset class="fieldset mb-4 rounded-box border border-base-300 bg-base-100 p-4 flex flex-wrap items-end gap-4" disabled={busy}>
		<legend class="fieldset-legend">Rent change</legend>
		<Field label="New rent from" id="rc-date"><input class="input" id="rc-date" type="date" bind:value={rcDate} required /></Field>
		<Field label="Rent ($)" id="rc-amount">
			<input class="input" id="rc-amount" type="number" min="0" step="0.01" bind:value={rcAmount} required />
		</Field>
		<Button type="submit">Change rent</Button>
		</fieldset>
	</form>
	{:else if action === 'Add charge'}
	<form
		onsubmit={(e) =>
			act(
				e,
				'charges',
				{ description: chDesc, amount: cents(chAmount), start_date: chStart, end_date: chEnd || undefined },
				'Charge added.'
			)}
	>
		<fieldset class="fieldset mb-4 rounded-box border border-base-300 bg-base-100 p-4 flex flex-wrap items-end gap-4" disabled={busy}>
		<legend class="fieldset-legend">Lease charge</legend>
		<Field label="Charge" id="ch-desc"><input class="input" id="ch-desc" bind:value={chDesc} required /></Field>
		<Field label="Amount per period ($)" id="ch-amount">
			<input class="input" id="ch-amount" type="number" min="0" step="0.01" bind:value={chAmount} required />
		</Field>
		<Field label="From" id="ch-start"><input class="input" id="ch-start" type="date" bind:value={chStart} required /></Field>
		<Field label="To (optional)" id="ch-end"><input class="input" id="ch-end" type="date" bind:value={chEnd} /></Field>
		<Button type="submit">Add charge</Button>
		</fieldset>
	</form>
	{:else if action === 'Record lodgement'}
		<form
			onsubmit={(e) => act(e, 'bond-lodgement', { rta_ref: bondRef, lodged_on: bondDate }, 'Bond lodged.')}
		>
			<fieldset class="fieldset mb-4 rounded-box border border-base-300 bg-base-100 p-4 flex flex-wrap items-end gap-4" disabled={busy}>
			<legend class="fieldset-legend">Bond lodgement</legend>
			<Field label="RTA reference" id="bond-ref"><input class="input" id="bond-ref" bind:value={bondRef} required /></Field>
			<Field label="Lodged on" id="bond-date">
				<input class="input" id="bond-date" type="date" bind:value={bondDate} required />
			</Field>
			<Button type="submit">Record lodgement</Button>
			</fieldset>
		</form>
	{:else if action === 'Terminate lease'}
	<form
		onsubmit={(e) => {
			if (!confirm(`Terminate this lease on ${termDate}?`)) {
				e.preventDefault();
				return;
			}
			act(e, 'terminate', { termination_date: termDate }, 'Lease terminated.');
		}}
	>
		<fieldset class="fieldset mb-4 rounded-box border border-base-300 bg-base-100 p-4 flex flex-wrap items-end gap-4" disabled={busy}>
		<legend class="fieldset-legend">Lease termination</legend>
		<Field label="Terminate on" id="term-date">
			<input class="input" id="term-date" type="date" bind:value={termDate} required />
		</Field>
		<Button type="submit" variant="danger">Terminate lease</Button>
		</fieldset>
	</form>
	{/if}
	<div class="flex justify-end"><Button variant="secondary" disabled={busy} onclick={close}>Cancel</Button></div>
	{/if}
	{/snippet}
	</Dialog>
{/if}

{#if notice}
	<Toast message={notice} onDismiss={() => (notice = '')} />
{/if}
{#if error && !actionOpen && loadedURL === url}
	<Toast message={error} tone="danger" onDismiss={() => (error = '')} />
{/if}

<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { api, ApiError } from '../../../../lib/api';
	import { money, date } from '../../../../lib/format';
	import PageHeader from '../../../../lib/components/PageHeader.svelte';
	import Button from '../../../../lib/components/Button.svelte';
	import EmptyState from '../../../../lib/components/EmptyState.svelte';
	import StatusBadge from '../../../../lib/components/StatusBadge.svelte';
	import Money from '../../../../lib/components/Money.svelte';
	import DateText from '../../../../lib/components/DateText.svelte';
	import Toast from '../../../../lib/components/Toast.svelte';
	import PaymentDialog, {
		type NewPayment
	} from '../../../../lib/modules/core/invoices/PaymentDialog.svelte';

	type Line = {
		id: number;
		description: string;
		quantity_milli: number;
		unit_amount: number;
		gst_code: string;
		gst_amount: number;
		amount: number;
	};

	type Payment = {
		id: number;
		amount: number;
		paid_on: string;
		method: string;
	};

	type InvoiceDetail = {
		id: number;
		contact_name: string;
		number: number | null;
		status: string;
		issue_date: string | null;
		due_date: string | null;
		subtotal: number;
		gst_total: number;
		total: number;
		amount_paid: number;
		outstanding: number;
		pay_options: string[];
		lines: Line[];
		payments: Payment[];
		provider_url: string;
	};

	const STATUS_TONE: Record<string, 'success' | 'warning' | 'danger' | 'info'> = {
		draft: 'info',
		open: 'warning',
		paid: 'success',
		void: 'danger'
	};

	const METHOD_LABEL: Record<string, string> = {
		bank_transfer: 'Bank transfer',
		payid: 'PayID',
		cash: 'Cash'
	};

	let loadStatus = $state<'loading' | 'error' | 'ready'>('loading');
	let errorMessage = $state('');
	let invoice = $state<InvoiceDetail | null>(null);
	let actionError = $state('');
	let actionBusy = $state(false);
	let paymentDialogOpen = $state(false);

	// canSend: an issued invoice can only be (re)sent to the payment
	// provider if it never reached one (no provider_url yet) and the org's
	// pay options actually include Stripe - matches the controller's rule
	// for this field.
	const canSend = $derived(
		invoice !== null &&
			invoice.status === 'open' &&
			invoice.provider_url === '' &&
			invoice.pay_options.includes('stripe')
	);

	async function load() {
		loadStatus = 'loading';
		errorMessage = '';
		try {
			const org = page.params.org;
			invoice = await api<InvoiceDetail>('GET', `/api/v1/orgs/${org}/invoices/${page.params.id}`);
			loadStatus = 'ready';
		} catch (e) {
			errorMessage = e instanceof ApiError ? e.message : 'Something went wrong.';
			loadStatus = 'error';
		}
	}

	onMount(load);

	// quantity formats a line's quantity_milli (thousandths of a unit) back
	// to a decimal for display only, e.g. 1500 -> "1.5" - never used for any
	// further calculation, which stays entirely server-side.
	function quantity(milli: number): string {
		return (milli / 1000).toFixed(3).replace(/\.?0+$/, '') || '0';
	}

	async function runAction(action: () => Promise<void>) {
		if (actionBusy) return;
		actionBusy = true;
		actionError = '';
		try {
			await action();
			await load();
		} catch (e) {
			actionError = e instanceof ApiError ? e.message : 'Something went wrong.';
		} finally {
			actionBusy = false;
		}
	}

	function issue() {
		const org = page.params.org;
		runAction(async () => {
			await api('POST', `/api/v1/orgs/${org}/invoices/${page.params.id}/issue`);
		});
	}

	function sendInvoice() {
		const org = page.params.org;
		runAction(async () => {
			await api('POST', `/api/v1/orgs/${org}/invoices/${page.params.id}/send`);
		});
	}

	function voidInvoice() {
		if (!window.confirm('Void this invoice? This cannot be undone.')) return;
		const org = page.params.org;
		runAction(async () => {
			await api('POST', `/api/v1/orgs/${org}/invoices/${page.params.id}/void`);
		});
	}

	function recordPayment(payment: NewPayment) {
		const org = page.params.org;
		runAction(async () => {
			await api('POST', `/api/v1/orgs/${org}/invoices/${page.params.id}/payments`, payment);
			paymentDialogOpen = false;
		});
	}
</script>

{#if loadStatus === 'loading'}
	<p aria-live="polite">Loading invoice…</p>
{:else if loadStatus === 'error'}
	<EmptyState title="Couldn't load the invoice" description={errorMessage}>
		{#snippet action()}
			<Button onclick={load}>Retry</Button>
		{/snippet}
	</EmptyState>
{:else if invoice}
	{@const inv = invoice}
	<PageHeader title={inv.number !== null ? `Invoice #${inv.number}` : 'Draft invoice'}>
		{#snippet actions()}
			<StatusBadge status={inv.status} tone={STATUS_TONE[inv.status] ?? 'info'} />
		{/snippet}
	</PageHeader>

	<p class="contact">{inv.contact_name}</p>

	<div class="dates">
		{#if inv.issue_date}<span>Issued <DateText iso={inv.issue_date} /></span>{/if}
		{#if inv.due_date}<span>Due <DateText iso={inv.due_date} /></span>{/if}
	</div>

	<section class="actions-row" aria-label="Invoice actions">
		{#if inv.status === 'draft'}
			<Button disabled={actionBusy} onclick={issue}>Issue</Button>
			<Button variant="danger" disabled={actionBusy} onclick={voidInvoice}>Void</Button>
		{:else if inv.status === 'open'}
			{#if canSend}
				<Button disabled={actionBusy} onclick={sendInvoice}>Send</Button>
			{/if}
			<Button variant="secondary" disabled={actionBusy} onclick={() => (paymentDialogOpen = true)}>
				Record payment
			</Button>
			<Button variant="danger" disabled={actionBusy} onclick={voidInvoice}>Void</Button>
		{/if}
	</section>

	<h2>Lines</h2>
	<table class="lines">
		<thead>
			<tr>
				<th scope="col">Description</th>
				<th scope="col">Quantity</th>
				<th scope="col">Unit price</th>
				<th scope="col">GST</th>
				<th scope="col">Amount</th>
			</tr>
		</thead>
		<tbody>
			{#each inv.lines as line (line.id)}
				<tr>
					<td>{line.description}</td>
					<td>{quantity(line.quantity_milli)}</td>
					<td><Money cents={line.unit_amount} /></td>
					<td>{line.gst_code}</td>
					<td><Money cents={line.amount} /></td>
				</tr>
			{/each}
		</tbody>
	</table>

	<dl class="totals">
		<div><dt>Subtotal</dt><dd><Money cents={inv.subtotal} /></dd></div>
		<div><dt>GST</dt><dd><Money cents={inv.gst_total} /></dd></div>
		<div><dt>Total</dt><dd><Money cents={inv.total} /></dd></div>
		<div><dt>Paid</dt><dd><Money cents={inv.amount_paid} /></dd></div>
		<div><dt>Outstanding</dt><dd><Money cents={inv.outstanding} /></dd></div>
	</dl>

	{#if inv.pay_options.length > 0}
		<p class="pay-options">Pay options: {inv.pay_options.join(', ')}</p>
	{/if}

	{#if inv.provider_url}
		<p><a href={inv.provider_url} target="_blank" rel="noopener noreferrer">Pay online</a></p>
	{/if}

	<h2>Payments</h2>
	{#if inv.payments.length === 0}
		<p class="no-payments">No payments recorded.</p>
	{:else}
		<table class="payments">
			<thead>
				<tr>
					<th scope="col">Date</th>
					<th scope="col">Method</th>
					<th scope="col">Amount</th>
				</tr>
			</thead>
			<tbody>
				{#each inv.payments as payment (payment.id)}
					<tr>
						<td>{date(payment.paid_on)}</td>
						<td>{METHOD_LABEL[payment.method] ?? payment.method}</td>
						<td><Money cents={payment.amount} /></td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}

	<PaymentDialog
		bind:open={paymentDialogOpen}
		outstandingCents={inv.outstanding}
		onSubmit={recordPayment}
	/>
{/if}

{#if actionError}
	<Toast message={actionError} tone="danger" onDismiss={() => (actionError = '')} />
{/if}

<style>
	.contact {
		color: var(--text-muted);
		margin: 0 0 var(--space-2);
	}
	.dates {
		display: flex;
		gap: var(--space-4);
		color: var(--text-muted);
		font-size: var(--text-sm);
		margin-bottom: var(--space-4);
	}
	.actions-row {
		display: flex;
		gap: var(--space-3);
		margin-bottom: var(--space-6);
	}
	table {
		width: 100%;
		border-collapse: collapse;
		margin-bottom: var(--space-6);
	}
	th,
	td {
		text-align: left;
		padding: var(--space-2) var(--space-3);
		border-bottom: 1px solid var(--border);
	}
	th {
		color: var(--text-muted);
		font-size: var(--text-sm);
		font-weight: 600;
	}
	.totals {
		display: flex;
		flex-direction: column;
		gap: var(--space-1);
		max-width: 20rem;
		margin-bottom: var(--space-6);
	}
	.totals div {
		display: flex;
		justify-content: space-between;
	}
	.totals dt,
	.totals dd {
		margin: 0;
	}
	.pay-options {
		color: var(--text-muted);
		font-size: var(--text-sm);
	}
	.no-payments {
		color: var(--text-muted);
	}

	@media (max-width: 639px) {
		thead {
			display: none;
		}
		table,
		tbody,
		tr,
		td {
			display: block;
			width: 100%;
		}
		tr {
			border: 1px solid var(--border);
			border-radius: var(--radius-md);
			margin-bottom: var(--space-3);
			padding: var(--space-2);
		}
		td {
			border-bottom: none;
		}
	}
</style>

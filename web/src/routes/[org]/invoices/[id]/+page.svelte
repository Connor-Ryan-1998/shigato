<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError } from '../../../../lib/api';
	import { date } from '../../../../lib/format';
	import PageHeader from '../../../../lib/components/PageHeader.svelte';
	import Button from '../../../../lib/components/Button.svelte';
	import EmptyState from '../../../../lib/components/EmptyState.svelte';
	import StatusBadge from '../../../../lib/components/StatusBadge.svelte';
	import Money from '../../../../lib/components/Money.svelte';
	import DateText from '../../../../lib/components/DateText.svelte';
	import Toast from '../../../../lib/components/Toast.svelte';
	import DataTable from '../../../../lib/components/DataTable.svelte';
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
	let paymentError = $state('');
	// True only for a background re-fetch after an action (issue/send/void/
	// payment) succeeds. Kept separate from loadStatus so a successful
	// action never drops the already-rendered invoice back to the
	// full-page "Loading…" state - that would unmount the whole section
	// (losing scroll position and keyboard focus) for a refresh the user
	// didn't ask to wait through.
	let refreshing = $state(false);

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

	async function load(opts: { background?: boolean } = {}) {
		if (opts.background) {
			refreshing = true;
		} else {
			loadStatus = 'loading';
			errorMessage = '';
		}
		try {
			const org = page.params.org;
			const result = await api<InvoiceDetail>(
				'GET',
				`/api/v1/orgs/${org}/invoices/${page.params.id}`
			);
			invoice = result;
			loadStatus = 'ready';
		} catch (e) {
			const message = e instanceof ApiError ? e.message : 'Something went wrong.';
			if (opts.background) {
				// The page is already showing the invoice from before the
				// action - surface the refresh failure the same way an
				// action failure is surfaced, without tearing the page down.
				actionError = message;
			} else {
				errorMessage = message;
				loadStatus = 'error';
			}
		} finally {
			refreshing = false;
		}
	}

	// Loads on mount, and reloads (full loading state) if the route's own
	// [id] param changes - e.g. navigating from one invoice's detail page
	// straight to another's, which SvelteKit can do without remounting this
	// component.
	$effect(() => {
		page.params.id;
		load();
	});

	// quantity formats a line's quantity_milli (thousandths of a unit) back
	// to a decimal for display only, e.g. 1500 -> "1.5" - never used for any
	// further calculation, which stays entirely server-side.
	function quantity(milli: number): string {
		return (milli / 1000).toFixed(3).replace(/\.?0+$/, '') || '0';
	}

	// runAction is for the actions that have nowhere else to show an error
	// (Issue/Send/Void aren't behind a dialog), so a page-level Toast is
	// fine for them.
	async function runAction(action: () => Promise<void>) {
		if (actionBusy) return;
		actionBusy = true;
		actionError = '';
		try {
			await action();
			await load({ background: true });
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

	function openPaymentDialog() {
		paymentError = '';
		paymentDialogOpen = true;
	}

	// Record payment is handled separately from runAction: on failure (e.g.
	// the 409 "invoice has payments; record a credit note instead"), the
	// dialog must stay open and show the server's message inside itself -
	// a page-level Toast would be invisible behind the native <dialog>'s
	// backdrop.
	async function recordPayment(payment: NewPayment) {
		if (actionBusy) return;
		actionBusy = true;
		paymentError = '';
		try {
			const org = page.params.org;
			await api('POST', `/api/v1/orgs/${org}/invoices/${page.params.id}/payments`, payment);
			paymentDialogOpen = false;
			await load({ background: true });
		} catch (e) {
			paymentError = e instanceof ApiError ? e.message : 'Something went wrong.';
		} finally {
			actionBusy = false;
		}
	}
</script>

{#snippet descCell(line: Line)}{line.description}{/snippet}
{#snippet quantityCell(line: Line)}{quantity(line.quantity_milli)}{/snippet}
{#snippet unitAmountCell(line: Line)}<Money cents={line.unit_amount} />{/snippet}
{#snippet gstCodeCell(line: Line)}{line.gst_code}{/snippet}
{#snippet lineAmountCell(line: Line)}<Money cents={line.amount} />{/snippet}

{#snippet paidOnCell(payment: Payment)}{date(payment.paid_on)}{/snippet}
{#snippet methodCell(payment: Payment)}{METHOD_LABEL[payment.method] ?? payment.method}{/snippet}
{#snippet paymentAmountCell(payment: Payment)}<Money cents={payment.amount} />{/snippet}

{#if loadStatus === 'loading'}
	<PageHeader title="Invoice" back={{ href: `/${page.params.org}/invoices`, label: 'Back to invoices' }} />
	<p aria-live="polite">Loading invoice…</p>
{:else if loadStatus === 'error'}
	<PageHeader title="Invoice" back={{ href: `/${page.params.org}/invoices`, label: 'Back to invoices' }} />
	<EmptyState title="Couldn't load the invoice" description={errorMessage}>
		{#snippet action()}
			<Button onclick={() => load()}>Retry</Button>
		{/snippet}
	</EmptyState>
{:else if invoice}
	{@const inv = invoice}
	<PageHeader title={inv.number !== null ? `Invoice #${inv.number}` : 'Draft invoice'} back={{ href: `/${page.params.org}/invoices`, label: 'Back to invoices' }}>
		{#snippet actions()}
			<StatusBadge status={inv.status} tone={STATUS_TONE[inv.status] ?? 'info'} />
		{/snippet}
	</PageHeader>

	{#if refreshing}
		<p class="mb-3 text-sm text-base-content/70" aria-live="polite">Refreshing…</p>
	{/if}

	<p class="mb-2 text-base-content/70">{inv.contact_name}</p>

	<div class="mb-4 flex gap-4 text-sm text-base-content/70">
		{#if inv.issue_date}<span>Issued <DateText iso={inv.issue_date} /></span>{/if}
		{#if inv.due_date}<span>Due <DateText iso={inv.due_date} /></span>{/if}
	</div>

	<section class="mb-6 flex flex-wrap gap-3" aria-label="Invoice actions">
		{#if inv.status === 'draft'}
			<Button disabled={actionBusy} onclick={issue}>Issue</Button>
			<Button variant="danger" disabled={actionBusy} onclick={voidInvoice}>Void</Button>
		{:else if inv.status === 'open'}
			{#if canSend}
				<Button disabled={actionBusy} onclick={sendInvoice}>Send</Button>
			{/if}
			<Button variant="secondary" disabled={actionBusy} onclick={openPaymentDialog}>
				Record payment
			</Button>
			<Button variant="danger" disabled={actionBusy} onclick={voidInvoice}>Void</Button>
		{/if}
	</section>

	<h2 class="mt-6 mb-3 text-lg font-semibold">Lines</h2>
	<DataTable
		columns={[
			{ key: 'description', label: 'Description', cell: descCell },
			{ key: 'quantity', label: 'Quantity', cell: quantityCell },
			{ key: 'unit_amount', label: 'Unit price', cell: unitAmountCell },
			{ key: 'gst_code', label: 'GST', cell: gstCodeCell },
			{ key: 'amount', label: 'Amount', cell: lineAmountCell }
		]}
		rows={inv.lines}
		rowKey={(line) => line.id}
	/>

	<dl class="my-6 flex max-w-xs flex-col gap-1 *:flex *:justify-between [&_div:nth-child(3)]:font-semibold">
		<div><dt>Subtotal</dt><dd><Money cents={inv.subtotal} /></dd></div>
		<div><dt>GST</dt><dd><Money cents={inv.gst_total} /></dd></div>
		<div><dt>Total</dt><dd><Money cents={inv.total} /></dd></div>
		<div><dt>Paid</dt><dd><Money cents={inv.amount_paid} /></dd></div>
		<div><dt>Outstanding</dt><dd><Money cents={inv.outstanding} /></dd></div>
	</dl>

	{#if inv.pay_options.length > 0}
		<p class="text-sm text-base-content/70">Pay options: {inv.pay_options.join(', ')}</p>
	{/if}

	{#if inv.provider_url}
		<p class="my-2"><a class="link link-primary" href={inv.provider_url} target="_blank" rel="noopener noreferrer">Pay online</a></p>
	{/if}

	<h2 class="mt-6 mb-3 text-lg font-semibold">Payments</h2>
	{#if inv.payments.length === 0}
		<p class="text-base-content/70">No payments recorded.</p>
	{:else}
		<DataTable
			columns={[
				{ key: 'paid_on', label: 'Date', cell: paidOnCell },
				{ key: 'method', label: 'Method', cell: methodCell },
				{ key: 'amount', label: 'Amount', cell: paymentAmountCell }
			]}
			rows={inv.payments}
			rowKey={(payment) => payment.id}
		/>
	{/if}

	<PaymentDialog
		bind:open={paymentDialogOpen}
		outstandingCents={inv.outstanding}
		error={paymentError}
		busy={actionBusy}
		onSubmit={recordPayment}
	/>
{/if}

{#if actionError}
	<Toast message={actionError} tone="danger" onDismiss={() => (actionError = '')} />
{/if}

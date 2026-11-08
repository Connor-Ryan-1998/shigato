<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError } from '../../../../../lib/api';
	import PageHeader from '../../../../../lib/components/PageHeader.svelte';
	import Button from '../../../../../lib/components/Button.svelte';
	import EmptyState from '../../../../../lib/components/EmptyState.svelte';
	import StatusBadge from '../../../../../lib/components/StatusBadge.svelte';
	import Money from '../../../../../lib/components/Money.svelte';
	import DateText from '../../../../../lib/components/DateText.svelte';
	import DataTable from '../../../../../lib/components/DataTable.svelte';

	type Payment = { id: number; amount: number; paid_on: string; method: string };

	type InvoiceDetail = {
		id: number;
		number: number | null;
		status: string;
		issue_date: string | null;
		due_date: string | null;
		total: number;
		amount_paid: number;
		outstanding: number;
		payment_reference: string;
		pay_options: string[];
		payments: Payment[];
		provider_url: string;
		bank_bsb?: string;
		bank_account?: string;
		payid?: string;
	};

	const STATUS_TONE: Record<string, 'success' | 'warning' | 'danger' | 'info'> = {
		open: 'warning',
		paid: 'success',
		uncollectible: 'danger'
	};
	const METHOD_LABEL: Record<string, string> = {
		bank_transfer: 'Bank transfer',
		payid: 'PayID',
		cash: 'Cash'
	};

	let loadStatus = $state<'loading' | 'error' | 'ready'>('loading');
	let errorMessage = $state('');
	let invoice = $state<InvoiceDetail | null>(null);
	// Which copy button was last clicked - not reset on a timer, matching
	// InviteDialog.svelte's "Copied" label, which also just stays until
	// something else is copied.
	let copiedField = $state('');

	// Bumped on every load() call; a response is only applied if it's still
	// the most recent request by the time it resolves - same pattern as
	// [org]/settings/members/+page.svelte. Without this, a fast id-to-id hop
	// (this invoice -> that invoice) could have the first (slower) request's
	// response land after the second's and clobber it with the wrong
	// invoice.
	let requestSeq = 0;

	async function load() {
		const seq = ++requestSeq;
		loadStatus = 'loading';
		errorMessage = '';
		try {
			const org = page.params.org;
			const id = page.params.id;
			const result = await api<InvoiceDetail>('GET', `/api/v1/portal/${org}/invoices/${id}`);
			if (seq !== requestSeq) return;
			invoice = result;
			loadStatus = 'ready';
		} catch (e) {
			if (seq !== requestSeq) return;
			errorMessage = e instanceof ApiError ? e.message : 'Something went wrong.';
			loadStatus = 'error';
		}
	}

	$effect(() => {
		page.params.id;
		load();
	});

	async function copy(value: string, field: string) {
		try {
			await navigator.clipboard.writeText(value);
			copiedField = field;
		} catch {
			copiedField = '';
		}
	}
</script>

{#snippet paidOnCell(payment: Payment)}<DateText iso={payment.paid_on} />{/snippet}
{#snippet methodCell(payment: Payment)}{METHOD_LABEL[payment.method] ?? payment.method}{/snippet}
{#snippet paymentAmountCell(payment: Payment)}<Money cents={payment.amount} />{/snippet}

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
	<PageHeader title={inv.number !== null ? `Invoice #${inv.number}` : 'Invoice'}>
		{#snippet actions()}
			<StatusBadge status={inv.status} tone={STATUS_TONE[inv.status] ?? 'info'} />
		{/snippet}
	</PageHeader>

	<div class="mb-4 flex gap-4 text-sm text-base-content/70">
		{#if inv.issue_date}<span>Issued <DateText iso={inv.issue_date} /></span>{/if}
		{#if inv.due_date}<span>Due <DateText iso={inv.due_date} /></span>{/if}
	</div>

	<dl class="mb-6 flex max-w-xs flex-col gap-1">
		<div class="flex justify-between"><dt>Total</dt><dd><Money cents={inv.total} /></dd></div>
		<div class="flex justify-between"><dt>Paid</dt><dd><Money cents={inv.amount_paid} /></dd></div>
		<div class="flex justify-between"><dt>Outstanding</dt><dd><Money cents={inv.outstanding} /></dd></div>
	</dl>

	<section class="card mb-6 max-w-md bg-base-100 shadow-sm" aria-label="Payment reference">
		<div class="card-body">
		<p class="font-semibold text-base-content/70">Payment reference</p>
		<div class="mb-2 flex items-center justify-between gap-3">
			<span class="text-lg font-semibold tabular-nums">{inv.payment_reference}</span>
			<Button variant="secondary" onclick={() => copy(inv.payment_reference, 'reference')}>
				{copiedField === 'reference' ? 'Copied' : 'Copy'}
			</Button>
		</div>
		</div>
	</section>

	{#if inv.provider_url}
		<p>
			<a class="btn btn-primary" href={inv.provider_url} target="_blank" rel="noopener noreferrer">
				Pay by card
			</a>
		</p>
	{/if}

	{#if inv.bank_bsb || inv.bank_account}
		<section aria-labelledby="bank-transfer-heading">
			<h2 id="bank-transfer-heading" class="mt-6 mb-2 text-lg font-semibold">Bank transfer</h2>
			{#if inv.bank_bsb}
				<div class="mb-2 flex items-center justify-between gap-3">
					<span>BSB {inv.bank_bsb}</span>
					<Button variant="secondary" onclick={() => copy(inv.bank_bsb ?? '', 'bsb')}>
						{copiedField === 'bsb' ? 'Copied' : 'Copy'}
					</Button>
				</div>
			{/if}
			{#if inv.bank_account}
				<div class="mb-2 flex items-center justify-between gap-3">
					<span>Account {inv.bank_account}</span>
					<Button variant="secondary" onclick={() => copy(inv.bank_account ?? '', 'account')}>
						{copiedField === 'account' ? 'Copied' : 'Copy'}
					</Button>
				</div>
			{/if}
		</section>
	{/if}

	{#if inv.payid}
		<section aria-labelledby="payid-heading">
			<h2 id="payid-heading" class="mt-6 mb-2 text-lg font-semibold">PayID</h2>
			<div class="mb-2 flex items-center justify-between gap-3">
				<span>{inv.payid}</span>
				<Button variant="secondary" onclick={() => copy(inv.payid ?? '', 'payid')}>
					{copiedField === 'payid' ? 'Copied' : 'Copy'}
				</Button>
			</div>
		</section>
	{/if}

	<h2 class="mt-6 mb-2 text-lg font-semibold">Payments</h2>
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
{/if}

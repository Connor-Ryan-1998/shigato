<script lang="ts" module>
	export type NewPayment = {
		amount: number;
		paid_on: string;
		method: string;
	};
</script>

<script lang="ts">
	import Dialog from '../../../components/Dialog.svelte';
	import Field from '../../../components/Field.svelte';
	import Select from '../../../components/Select.svelte';
	import Button from '../../../components/Button.svelte';
	import { parseCents, todayLocal } from '../../../format';

	// Matches the server's accepted methods (internal/httpapi/core_routes.go
	// recordPayment).
	const METHOD_OPTIONS = [
		{ value: 'bank_transfer', label: 'Bank transfer' },
		{ value: 'payid', label: 'PayID' },
		{ value: 'cash', label: 'Cash' }
	];

	let {
		open = $bindable(false),
		outstandingCents,
		error,
		busy = false,
		onSubmit
	}: {
		open?: boolean;
		outstandingCents: number;
		// The server's error message from the last failed submit (e.g. the
		// 409 "invoice has payments; record a credit note instead"). Shown
		// inside the dialog itself - a native <dialog>'s backdrop sits above
		// the rest of the page, so a page-level Toast would be invisible
		// while this is open.
		error?: string;
		busy?: boolean;
		onSubmit: (payment: NewPayment) => void;
	} = $props();

	let amount = $state('');
	let paidOn = $state(todayLocal());
	let method = $state('bank_transfer');

	// Re-seed the form from the invoice's current outstanding balance every
	// time the dialog opens, so re-opening it after a previous payment never
	// shows a stale amount left over from last time.
	$effect(() => {
		if (!open) return;
		amount = (outstandingCents / 100).toFixed(2);
		paidOn = todayLocal();
		method = 'bank_transfer';
	});

	const amountCents = $derived(parseCents(amount));
	const amountError = $derived(
		amount.trim() !== '' && amountCents === null
			? 'Enter a valid amount, up to 2 decimal places.'
			: undefined
	);
	const valid = $derived(amountCents !== null && amountCents > 0 && paidOn !== '');

	function submit() {
		if (amountCents === null) return;
		onSubmit({ amount: amountCents, paid_on: paidOn, method });
	}
</script>

<Dialog bind:open title="Record payment">
	<div class="fields">
		<Field label="Amount ($)" id="payment-amount" error={amountError}>
			<input id="payment-amount" inputmode="decimal" bind:value={amount} />
		</Field>
		<Field label="Date paid" id="payment-paid-on">
			<input id="payment-paid-on" type="date" bind:value={paidOn} />
		</Field>
		<Field label="Method" id="payment-method">
			<Select id="payment-method" bind:value={method} options={METHOD_OPTIONS} />
		</Field>
	</div>
	{#if error}
		<p class="dialog-error" role="alert">{error}</p>
	{/if}
	<div class="actions">
		<Button variant="secondary" disabled={busy} onclick={() => (open = false)}>Cancel</Button>
		<Button onclick={submit} disabled={!valid || busy}>Record payment</Button>
	</div>
</Dialog>

<style>
	.fields {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
		margin-bottom: var(--space-4);
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-3);
	}
	.dialog-error {
		margin: 0 0 var(--space-4);
		padding: var(--space-2) var(--space-3);
		border-radius: var(--radius-md);
		background: var(--danger-subtle);
		color: var(--danger);
	}
</style>

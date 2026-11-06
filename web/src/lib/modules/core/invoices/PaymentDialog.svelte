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
	import { parseCents } from '../../../format';

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
		onSubmit
	}: {
		open?: boolean;
		outstandingCents: number;
		onSubmit: (payment: NewPayment) => void;
	} = $props();

	function todayISO(): string {
		return new Date().toISOString().slice(0, 10);
	}

	let amount = $state('');
	let paidOn = $state(todayISO());
	let method = $state('bank_transfer');

	// Re-seed the form from the invoice's current outstanding balance every
	// time the dialog opens, so re-opening it after a previous payment never
	// shows a stale amount left over from last time.
	$effect(() => {
		if (!open) return;
		amount = (outstandingCents / 100).toFixed(2);
		paidOn = todayISO();
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
	<div class="actions">
		<Button variant="secondary" onclick={() => (open = false)}>Cancel</Button>
		<Button onclick={submit} disabled={!valid}>Record payment</Button>
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
</style>

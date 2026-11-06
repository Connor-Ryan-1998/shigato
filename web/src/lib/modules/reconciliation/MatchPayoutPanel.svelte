<script lang="ts">
	import Field from '../../components/Field.svelte';
	import Button from '../../components/Button.svelte';

	let {
		busy = false,
		onMatch
	}: {
		busy?: boolean;
		onMatch: (payoutRef: string) => void;
	} = $props();

	let payoutRef = $state('');

	function submit() {
		if (payoutRef.trim() === '') return;
		onMatch(payoutRef.trim());
	}
</script>

<div class="panel">
	<Field label="Stripe payout reference" id="match-payout-ref">
		<input id="match-payout-ref" bind:value={payoutRef} />
	</Field>
	<Button disabled={busy || payoutRef.trim() === ''} onclick={submit}>Match payout</Button>
</div>

<style>
	.panel {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
		align-items: flex-start;
	}
</style>

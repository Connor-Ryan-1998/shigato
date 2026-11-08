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

<div class="flex flex-col items-start gap-3">
	<Field label="Stripe payout reference" id="match-payout-ref">
		<input class="input w-full" id="match-payout-ref" bind:value={payoutRef} />
	</Field>
	<Button disabled={busy || payoutRef.trim() === ''} onclick={submit}>Match payout</Button>
</div>

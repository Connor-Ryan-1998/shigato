<script lang="ts">
	import { money } from '../format';

	let { cents }: { cents: number } = $props();
</script>

<span class="money">{money(cents)}</span>

<style>
	.money {
		font-variant-numeric: tabular-nums;
	}
</style>

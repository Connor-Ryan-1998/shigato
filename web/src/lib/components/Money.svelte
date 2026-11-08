<script lang="ts">
	import { money } from '../format';

	let { cents }: { cents: number } = $props();
</script>

<span class="tabular-nums">{money(cents)}</span>

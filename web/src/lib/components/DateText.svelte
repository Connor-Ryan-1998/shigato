<script lang="ts">
	import { date } from '../format';

	let { iso }: { iso: string } = $props();
</script>

<time datetime={iso}>{date(iso)}</time>

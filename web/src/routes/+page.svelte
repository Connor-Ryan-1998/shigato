<script lang="ts">
	import { goto } from '$app/navigation';
	import { session } from '../lib/session.svelte';

	$effect(() => {
		if (session.me) {
			const first = session.me.orgs[0];
			goto(first ? `/${first.slug}` : '/login');
		}
	});
</script>

<p>Loading…</p>

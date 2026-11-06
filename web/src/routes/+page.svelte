<script lang="ts">
	import { goto } from '$app/navigation';
	import { session } from '../lib/session.svelte';

	$effect(() => {
		if (session.me && session.me.orgs.length > 0) {
			goto(`/${session.me.orgs[0].slug}`);
		}
	});
</script>

{#if session.me && session.me.orgs.length === 0}
	<p>You don't have access to any organisation yet.</p>
{:else}
	<p>Loading…</p>
{/if}

<script lang="ts">
	import { goto } from '$app/navigation';
	import { session } from '../lib/session.svelte';

	// A user with staff access lands in the staff shell (their first staff
	// org, by name - the order /me already returns). A user with only
	// tenant/client memberships (every org has portal: true) goes to the
	// portal picker instead - orgs[0] can't be used directly, since /me
	// orders every org (staff and portal alike) by name together.
	$effect(() => {
		if (!session.me) return;
		const staffOrg = session.me.orgs.find((o) => !o.portal);
		if (staffOrg) {
			goto(`/${staffOrg.slug}`);
		} else if (session.me.orgs.length > 0) {
			goto('/portal');
		}
	});
</script>

{#if session.me && session.me.orgs.length === 0}
	<p class="p-8 text-center">You don't have access to any organisation yet.</p>
{:else}
	<p class="flex items-center justify-center gap-2 p-8">
		<span class="loading loading-spinner" aria-hidden="true"></span>Loading…
	</p>
{/if}

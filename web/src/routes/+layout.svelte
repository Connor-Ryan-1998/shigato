<script lang="ts">
	import '../lib/styles/tokens.css';
	import '../lib/styles/base.css';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { api, ApiError } from '../lib/api';
	import { session, type MeResponse } from '../lib/session.svelte';

	let { children } = $props();
	let loading = $state(true);

	onMount(async () => {
		// /login has no session to fetch yet; fetching /me there would just
		// 401 and bounce back to the very page we're already on.
		if (page.url.pathname === '/login') {
			loading = false;
			return;
		}
		try {
			const [me, meta] = await Promise.all([
				api<MeResponse>('GET', '/api/v1/me'),
				api<{ modules: string[] }>('GET', '/api/v1/meta')
			]);
			session.setMe(me);
			session.setModules(meta.modules);
		} catch (err) {
			// A 401 already triggered api()'s onUnauthorized redirect; any
			// other failure is unexpected, so let it surface.
			if (!(err instanceof ApiError && err.status === 401)) {
				throw err;
			}
		} finally {
			loading = false;
		}
	});
</script>

{#if loading}
	<p>Loading…</p>
{:else}
	{@render children()}
{/if}

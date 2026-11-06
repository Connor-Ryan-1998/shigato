<script lang="ts">
	import '../lib/styles/tokens.css';
	import '../lib/styles/base.css';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { api, ApiError, registerReloginPrompt } from '../lib/api';
	import { session, type MeResponse } from '../lib/session.svelte';
	import { theme } from '../lib/theme.svelte';
	import Button from '../lib/components/Button.svelte';
	import ReloginDialog from '../lib/components/ReloginDialog.svelte';

	let { children } = $props();
	let loading = $state(true);
	let error = $state('');

	let reloginOpen = $state(false);
	let resolveRelogin: ((ok: boolean) => void) | null = null;

	function handleReloginSuccess() {
		reloginOpen = false;
		resolveRelogin?.(true);
		resolveRelogin = null;
	}

	function handleReloginCancel() {
		reloginOpen = false;
		resolveRelogin?.(false);
		resolveRelogin = null;
	}

	async function loadSession() {
		error = '';
		loading = true;
		try {
			const [me, meta] = await Promise.all([
				api<MeResponse>('GET', '/api/v1/me'),
				api<{ modules: string[] }>('GET', '/api/v1/meta')
			]);
			session.setMe(me);
			session.setModules(meta.modules);
		} catch (err) {
			// A 401 already triggered api()'s onUnauthorized redirect; any
			// other failure is unexpected, but it must still leave the user
			// with something to do, not a silently rejected promise and a
			// stuck "Loading…".
			if (!(err instanceof ApiError && err.status === 401)) {
				error = err instanceof ApiError ? err.message : 'Something went wrong.';
			}
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		// Applied here (not just read at module init) so a change from the
		// theme toggle elsewhere in the app, and the very first paint, both
		// reflect the stored preference - including on /login, which never
		// mounts the org shell where the toggle lives.
		theme.apply();

		// api.ts's defaultOnUnauthorized already never calls this on /login
		// itself, so registering it unconditionally is harmless there; a 401
		// anywhere else now reopens this dialog in place instead of falling
		// back to a redirect.
		registerReloginPrompt(
			() =>
				new Promise<boolean>((resolve) => {
					resolveRelogin = resolve;
					reloginOpen = true;
				})
		);

		// /login has no session to fetch yet; fetching /me there would just
		// 401 and bounce back to the very page we're already on.
		if (page.url.pathname === '/login') {
			loading = false;
			return;
		}
		loadSession();

		return () => registerReloginPrompt(null);
	});
</script>

{#if loading}
	<p>Loading…</p>
{:else if error}
	<div class="session-error">
		<p role="alert">{error}</p>
		<Button onclick={loadSession}>Retry</Button>
	</div>
{:else}
	{@render children()}
{/if}

<ReloginDialog
	bind:open={reloginOpen}
	email={session.me?.user.email ?? ''}
	onSuccess={handleReloginSuccess}
	onCancel={handleReloginCancel}
/>

<style>
	.session-error {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: var(--space-4);
		padding: var(--space-8) var(--space-4);
		text-align: center;
	}
</style>

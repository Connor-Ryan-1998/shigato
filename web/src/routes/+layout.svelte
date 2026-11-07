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
				api<{ modules: string[]; sso: boolean }>('GET', '/api/v1/meta')
			]);
			session.setMe(me);
			session.setModules(meta.modules);
			session.setSso(meta.sso);
			// Only registered once there's a real session to pre-fill the
			// dialog's email from: a 401 before this point (e.g. the /me
			// call itself, on a cold load with an already-expired session)
			// must fall through to api.ts's plain redirect instead of
			// opening ReloginDialog with nothing to show.
			registerReloginPrompt(
				() =>
					new Promise<boolean>((resolve) => {
						resolveRelogin = resolve;
						reloginOpen = true;
					})
			);
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

		// Registered before any early return, so a component that never
		// gets as far as registering a prompt (the /login branch below, or
		// loadSession failing before it registers one) still tears down
		// cleanly - unregistering something that was never registered is a
		// harmless no-op.
		const cleanup = () => registerReloginPrompt(null);

		// /login and /auth/magic have no session to fetch yet; fetching /me
		// there would 401 before the page's own logic runs (the magic page
		// hasn't even posted its token yet) and bounce the browser straight
		// to /login via a hard navigation, never rendering either page.
		if (page.url.pathname === '/login' || page.url.pathname === '/auth/magic') {
			loading = false;
			return cleanup;
		}
		loadSession();

		return cleanup;
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
	sso={session.sso}
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

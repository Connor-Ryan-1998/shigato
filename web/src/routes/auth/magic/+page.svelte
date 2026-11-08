<script lang="ts">
	import { onMount } from 'svelte';
	import { goto, replaceState } from '$app/navigation';
	import { page } from '$app/state';
	import { api, ApiError } from '../../../lib/api';
	import { session, type MeResponse } from '../../../lib/session.svelte';

	// The SPA's own landing page for an emailed magic link
	// (GET /auth/magic?token=...): it posts the token to the verify
	// endpoint, which sets the session cookie, then loads the session the
	// same way the password login form does before leaving for /portal.
	let status = $state<'verifying' | 'error'>('verifying');
	let errorMessage = $state('');

	onMount(async () => {
		const token = page.url.searchParams.get('token');
		// Scrubbed from the URL immediately - before the verify call even
		// starts, and regardless of its outcome - so the single-use token
		// never lingers in browser history, a referrer header, or a
		// screenshot/share of this tab. replaceState (not goto) keeps this
		// as a no-navigation history edit.
		replaceState('/auth/magic', {});
		if (!token) {
			status = 'error';
			errorMessage = 'This link has expired — request a new one';
			return;
		}
		try {
			const result = await api<{ redirect: string }>('POST', '/api/v1/auth/magic/verify', {
				token
			});
			const [me, meta] = await Promise.all([
				api<MeResponse>('GET', '/api/v1/me'),
				api<{ modules: string[]; sso: boolean }>('GET', '/api/v1/meta')
			]);
			session.setMe(me);
			session.setModules(meta.modules);
			session.setSso(meta.sso);
			goto(result.redirect || '/portal', { replaceState: true });
		} catch (err) {
			status = 'error';
			errorMessage = err instanceof ApiError ? err.message : 'Something went wrong.';
		}
	});
</script>

<div class="flex min-h-screen items-center justify-center bg-base-200 p-4">
	<div class="card w-full max-w-sm bg-base-100 shadow">
		<div class="card-body items-center text-center">
	{#if status === 'verifying'}
		<p aria-live="polite">Signing you in…</p>
	{:else}
		<p role="alert" class="alert alert-error alert-soft">{errorMessage}</p>
		<p><a class="link link-primary" href="/login">Request a new link</a></p>
	{/if}
		</div>
	</div>
</div>

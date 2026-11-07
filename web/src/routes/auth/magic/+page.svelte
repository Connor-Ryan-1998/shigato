<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
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
			goto(result.redirect || '/portal');
		} catch (err) {
			status = 'error';
			errorMessage = err instanceof ApiError ? err.message : 'Something went wrong.';
		}
	});
</script>

<div class="magic-page">
	{#if status === 'verifying'}
		<p aria-live="polite">Signing you in…</p>
	{:else}
		<p role="alert">{errorMessage}</p>
		<p><a href="/login">Request a new link</a></p>
	{/if}
</div>

<style>
	.magic-page {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--space-3);
		min-height: 100vh;
		padding: var(--space-4);
		text-align: center;
	}
</style>

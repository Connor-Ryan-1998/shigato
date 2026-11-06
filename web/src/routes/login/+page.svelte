<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, ApiError } from '../../lib/api';
	import { session, type MeResponse } from '../../lib/session.svelte';
	import Field from '../../lib/components/Field.svelte';
	import Button from '../../lib/components/Button.svelte';

	let email = $state('');
	let password = $state('');
	let error = $state('');
	let submitting = $state(false);

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		submitting = true;
		try {
			await api('POST', '/api/v1/auth/login', { email, password });
			const [me, meta] = await Promise.all([
				api<MeResponse>('GET', '/api/v1/me'),
				api<{ modules: string[] }>('GET', '/api/v1/meta')
			]);
			session.setMe(me);
			session.setModules(meta.modules);

			const next = page.url.searchParams.get('next');
			if (next && next !== '/login') {
				goto(next);
			} else if (me.orgs[0]) {
				goto(`/${me.orgs[0].slug}`);
			} else {
				goto('/login');
			}
		} catch (err) {
			error = err instanceof ApiError ? err.message : 'Something went wrong.';
		} finally {
			submitting = false;
		}
	}
</script>

<div class="login-page">
	<form onsubmit={submit}>
		<h1>Sign in</h1>
		<Field label="Email" id="email">
			<input id="email" type="email" bind:value={email} required autocomplete="username" />
		</Field>
		<Field label="Password" id="password">
			<input
				id="password"
				type="password"
				bind:value={password}
				required
				autocomplete="current-password"
			/>
		</Field>
		{#if error}
			<p class="error" role="alert">{error}</p>
		{/if}
		<Button type="submit" disabled={submitting}>Sign in</Button>
	</form>
</div>

<style>
	.login-page {
		display: flex;
		justify-content: center;
		align-items: center;
		min-height: 100vh;
		padding: var(--space-4);
	}
	form {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		width: 100%;
		max-width: 24rem;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius-lg);
		padding: var(--space-6);
		box-shadow: var(--shadow-md);
	}
	.error {
		margin: 0;
		color: var(--danger);
	}
</style>

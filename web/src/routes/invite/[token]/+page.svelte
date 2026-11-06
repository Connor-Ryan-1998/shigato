<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../../lib/api';
	import { session, type MeResponse } from '../../../lib/session.svelte';
	import Field from '../../../lib/components/Field.svelte';
	import Button from '../../../lib/components/Button.svelte';

	type InviteDetails = { org_name: string; email: string; role: string; expired: boolean };

	let loadStatus = $state<'loading' | 'error' | 'notfound' | 'ready'>('loading');
	let details = $state<InviteDetails | null>(null);

	let name = $state('');
	let password = $state('');
	let error = $state('');
	let submitting = $state(false);
	let accepted = $state(false);

	const token = $derived(page.params.token ?? '');
	const alreadyLoggedIn = $derived(session.me !== null);

	async function load() {
		loadStatus = 'loading';
		try {
			details = await api<InviteDetails>('GET', `/api/v1/invites/${token}`);
			loadStatus = 'ready';
		} catch (e) {
			if (e instanceof ApiError && e.status === 404) {
				loadStatus = 'notfound';
			} else {
				loadStatus = 'error';
			}
		}
	}

	$effect(() => {
		token;
		load();
	});

	async function accept(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		submitting = true;
		try {
			const body = alreadyLoggedIn ? undefined : { name, password };
			await api('POST', `/api/v1/invites/${token}/accept`, body);
			accepted = true;
			if (!alreadyLoggedIn) {
				const [me, meta] = await Promise.all([
					api<MeResponse>('GET', '/api/v1/me'),
					api<{ modules: string[] }>('GET', '/api/v1/meta')
				]);
				session.setMe(me);
				session.setModules(meta.modules);
			}
			const org = session.me?.orgs.find((o) => o.name === details?.org_name);
			goto(org ? `/${org.slug}` : '/');
		} catch (err) {
			error = err instanceof ApiError ? err.message : 'Something went wrong.';
		} finally {
			submitting = false;
		}
	}
</script>

<div class="invite-page">
	<div class="card">
		{#if loadStatus === 'loading'}
			<p aria-live="polite">Loading invite…</p>
		{:else if loadStatus === 'notfound'}
			<h1>Invite not found</h1>
			<p>This invite link doesn't exist. Ask the person who sent it for a new one.</p>
		{:else if loadStatus === 'error'}
			<h1>Something went wrong</h1>
			<p>Couldn't load this invite. Try again in a moment.</p>
		{:else if details?.expired}
			<h1>This invite has expired</h1>
			<p>Ask an owner at {details.org_name} to send you a new invite.</p>
		{:else if accepted}
			<h1>You're in</h1>
			<p>Redirecting…</p>
		{:else if details}
			<h1>Join {details.org_name}</h1>
			<p>You've been invited as <strong>{details.role}</strong> ({details.email}).</p>
			{#if alreadyLoggedIn}
				<form onsubmit={accept}>
					{#if error}
						<p class="error" role="alert">{error}</p>
					{/if}
					<Button type="submit" disabled={submitting}>Accept invite</Button>
				</form>
			{:else}
				<form onsubmit={accept}>
					<Field label="Your name" id="invite-name">
						<input id="invite-name" bind:value={name} required autocomplete="name" />
					</Field>
					<Field label="Password" id="invite-password" hint="At least 12 characters.">
						<input
							id="invite-password"
							type="password"
							bind:value={password}
							required
							minlength="12"
							autocomplete="new-password"
						/>
					</Field>
					{#if error}
						<p class="error" role="alert">{error}</p>
					{/if}
					<Button type="submit" disabled={submitting}>Create account and accept</Button>
				</form>
			{/if}
		{/if}
	</div>
</div>

<style>
	.invite-page {
		display: flex;
		justify-content: center;
		align-items: center;
		min-height: 100vh;
		padding: var(--space-4);
	}
	.card {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		width: 100%;
		max-width: 28rem;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius-lg);
		padding: var(--space-6);
		box-shadow: var(--shadow-md);
	}
	form {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}
	h1 {
		margin: 0;
		font-size: var(--text-xl);
	}
	.error {
		margin: 0;
		color: var(--danger);
	}
</style>

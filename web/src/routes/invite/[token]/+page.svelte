<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../../lib/api';
	import { session, type MeResponse } from '../../../lib/session.svelte';
	import Field from '../../../lib/components/Field.svelte';
	import Button from '../../../lib/components/Button.svelte';

	type InviteDetails = {
		org_name: string;
		email: string;
		role: string;
		expired: boolean;
		// Ruling 63: true only for a signed-in request from a user with no
		// password (a portal-only account from a magic link) when OIDC is
		// disabled - they must set one to be able to sign back in as staff.
		needs_password: boolean;
	};

	let loadStatus = $state<'loading' | 'error' | 'notfound' | 'ready'>('loading');
	let details = $state<InviteDetails | null>(null);

	let name = $state('');
	let password = $state('');
	let error = $state('');
	let submitting = $state(false);
	let accepted = $state(false);
	let requestSeq = 0;

	const token = $derived(page.params.token ?? '');
	const alreadyLoggedIn = $derived(session.me !== null);

	async function load() {
		const seq = ++requestSeq;
		const current = token;
		loadStatus = 'loading';
		try {
			const result = await api<InviteDetails>('GET', `/api/v1/invites/${current}`);
			if (seq !== requestSeq || current !== token) return;
			details = result;
			loadStatus = 'ready';
		} catch (e) {
			if (seq !== requestSeq || current !== token) return;
			if (e instanceof ApiError && e.status === 404) {
				loadStatus = 'notfound';
			} else {
				loadStatus = 'error';
			}
		}
	}

	$effect(() => {
		token;
		name = ''; password = ''; error = ''; accepted = false; submitting = false;
		load();
		return () => { requestSeq++; };
	});

	async function accept(e: SubmitEvent) {
		e.preventDefault();
		if (submitting || accepted || loadStatus !== 'ready' || details?.expired) return;
		const seq = requestSeq;
		const current = token;
		error = '';
		submitting = true;
		try {
			const body = alreadyLoggedIn
				? details?.needs_password
					? { password }
					: undefined
				: { name, password };
			const res = await api<{ org_slug: string }>('POST', `/api/v1/invites/${token}/accept`, body);
			if (seq !== requestSeq || current !== token) return;
			accepted = true;
			// Always refetch /me: accepting while already logged in adds a new
			// org to the current session's membership list, and accepting
			// anonymously creates the session in the first place - either way
			// the shell's own cached /me is now stale.
			const [me, meta] = await Promise.all([
				api<MeResponse>('GET', '/api/v1/me'),
				api<{ modules: string[] }>('GET', '/api/v1/meta')
			]);
			if (seq !== requestSeq || current !== token) return;
			session.setMe(me);
			session.setModules(meta.modules);
			goto(`/${res.org_slug}`);
		} catch (err) {
			if (seq === requestSeq && current === token) error = err instanceof ApiError ? err.message : 'Something went wrong.';
		} finally {
			if (seq === requestSeq) submitting = false;
		}
	}
</script>

<div class="flex min-h-screen items-center justify-center bg-base-200 p-4">
	<div class="card w-full max-w-md bg-base-100 shadow">
		<div class="card-body">
		{#if loadStatus === 'loading'}
			<h1 class="card-title text-2xl">Invitation</h1>
			<p aria-live="polite">Loading invite…</p>
		{:else if loadStatus === 'notfound'}
			<h1 class="card-title text-2xl">Invite not found</h1>
			<p>This invite link doesn't exist. Ask the person who sent it for a new one.</p>
		{:else if loadStatus === 'error'}
			<h1 class="card-title text-2xl">Something went wrong</h1>
			<p>Couldn't load this invite. Try again in a moment.</p>
			<Button variant="secondary" onclick={load}>Retry</Button>
		{:else if details?.expired}
			<h1 class="card-title text-2xl">This invite has expired</h1>
			<p>Ask an owner at {details.org_name} to send you a new invite.</p>
		{:else if accepted}
			<h1 class="card-title text-2xl">You're in</h1>
			<p>Redirecting…</p>
		{:else if details}
			<h1 class="card-title text-2xl">Join {details.org_name}</h1>
			<p>You've been invited as <strong>{details.role}</strong> ({details.email}).</p>
			{#if alreadyLoggedIn}
				<form onsubmit={accept} class="flex flex-col gap-4">
					<fieldset disabled={submitting} class="grid min-w-0 gap-4">
					{#if details.needs_password}
						<Field label="Set a password" id="invite-password" hint="At least 12 characters.">
							<input
								class="input w-full"
								id="invite-password"
								type="password"
								bind:value={password}
								required
								minlength="12"
								autocomplete="new-password"
							/>
						</Field>
					{/if}
					{#if error}
						<p class="alert alert-error alert-soft" role="alert">{error}</p>
					{/if}
					<Button type="submit" disabled={submitting}>Accept invite</Button>
					</fieldset>
				</form>
			{:else}
				<form onsubmit={accept} class="flex flex-col gap-4">
					<fieldset disabled={submitting} class="grid min-w-0 gap-4">
					<Field label="Your name" id="invite-name">
						<input class="input w-full" id="invite-name" bind:value={name} required autocomplete="name" />
					</Field>
					<Field label="Password" id="invite-password" hint="At least 12 characters.">
						<input
							class="input w-full"
							id="invite-password"
							type="password"
							bind:value={password}
							required
							minlength="12"
							autocomplete="new-password"
						/>
					</Field>
					{#if error}
						<p class="alert alert-error alert-soft" role="alert">{error}</p>
					{/if}
					<Button type="submit" disabled={submitting}>Create account and accept</Button>
					</fieldset>
				</form>
			{/if}
		{/if}
		</div>
	</div>
</div>

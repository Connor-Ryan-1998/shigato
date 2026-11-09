<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, ApiError } from '../../lib/api';
	import { session, type MeResponse } from '../../lib/session.svelte';
	import Field from '../../lib/components/Field.svelte';
	import Button from '../../lib/components/Button.svelte';

	// Fixed messages for the OIDC callback's error codes
	// (/login?error=<code>). Never derived from IdP text - the backend only
	// ever sends one of these codes, never a message. An unknown code is
	// ignored (no error shown) rather than leaking the raw code to the user.
	const ssoErrorMessages: Record<string, string> = {
		sso_expired: 'Your sign-in attempt expired. Try again.',
		sso_unavailable: 'Single sign-on is temporarily unavailable.',
		sso_failed: 'Single sign-on failed. Try again, or sign in with your password.',
		email_unverified: 'Your identity provider account has an unverified email address.',
		not_invited: 'No shigoto account for this email — ask an owner to invite you.',
		not_staff: 'This account has no staff access.',
		sso_conflict: 'This identity is already linked to a different account.'
	};

	let email = $state('');
	let password = $state('');
	let error = $state(ssoErrorMessages[page.url.searchParams.get('error') ?? ''] ?? '');
	let submitting = $state(false);
	let sso = $state(false);

	let magicEmail = $state('');
	let magicSubmitting = $state(false);
	let magicError = $state('');
	let magicSent = $state(false);

	function oidcStartUrl(): string {
		const next = safeNext(page.url.searchParams.get('next')) ?? '/';
		return '/api/v1/auth/oidc/start?next=' + encodeURIComponent(next);
	}

	// Only a same-site, absolute path is a safe redirect target: a bare
	// "//evil.com" is parsed by browsers as a protocol-relative URL (an
	// open redirect), and anything not starting with "/" could be an
	// absolute URL to another origin entirely. A backslash is rejected too
	// (Ruling 57, matching the backend's safeOIDCNext in internal/auth/oidc.go):
	// some browsers normalize an interior backslash to "/" before resolving
	// a URL, so "/\evil.com" or a tab/newline-smuggled "/\t/evil.com" can
	// otherwise reach a different origin once followed. Control characters
	// are rejected for the same reason.
	function safeNext(raw: string | null): string | null {
		if (!raw || raw === '/login') return null;
		if (!raw.startsWith('/') || raw.startsWith('//')) return null;
		for (let i = 0; i < raw.length; i++) {
			const code = raw.charCodeAt(i);
			if (raw[i] === '\\' || code < 0x20 || code === 0x7f) return null;
		}
		return raw;
	}

	onMount(() => {
		api<{ modules: string[]; sso: boolean }>('GET', '/api/v1/meta')
			.then((meta) => {
				sso = meta.sso;
			})
			.catch(() => {
				// /meta is unauthenticated and shouldn't ever fail here, but if
				// it does, just leave the SSO option hidden rather than
				// blocking the password form.
			});
	});

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (submitting || magicSubmitting) return;
		error = '';
		submitting = true;
		try {
			await api('POST', '/api/v1/auth/login', { email, password });
			const [me, meta] = await Promise.all([
				api<MeResponse>('GET', '/api/v1/me'),
				api<{ modules: string[]; sso: boolean }>('GET', '/api/v1/meta')
			]);
			session.setMe(me);
			session.setModules(meta.modules);
			session.setSso(meta.sso);

			const next = safeNext(page.url.searchParams.get('next'));
			if (next) {
				goto(next);
			} else if (me.orgs[0]) {
				goto(`/${me.orgs[0].slug}`);
			} else {
				goto('/');
			}
		} catch (err) {
			error = err instanceof ApiError ? err.message : 'Something went wrong.';
		} finally {
			submitting = false;
		}
	}

	// Ruling: /auth/magic always returns 202 whether or not the email is
	// known (global-constraints.md "no account enumeration"), so magicSent
	// shows the same confirmation either way. A 422 ("email is not
	// configured") or 429 (rate limited) is a real, visible failure - not
	// an enumeration risk - so those show the server's own message.
	async function submitMagic(e: SubmitEvent) {
		e.preventDefault();
		if (magicSubmitting || submitting) return;
		magicError = '';
		magicSubmitting = true;
		try {
			await api('POST', '/api/v1/auth/magic', { email: magicEmail });
			magicSent = true;
		} catch (err) {
			magicError = err instanceof ApiError ? err.message : 'Something went wrong.';
		} finally {
			magicSubmitting = false;
		}
	}
</script>

<div class="flex min-h-screen items-center justify-center bg-base-200 p-4">
	<div class="flex w-full max-w-sm flex-col gap-4">
	<form onsubmit={submit} class="card-body card bg-base-100 shadow">
		<h1 class="card-title text-2xl">Sign in</h1>
		<fieldset disabled={submitting || magicSubmitting} class="grid min-w-0 gap-4">
		<Field label="Email" id="email">
			<input class="input w-full" id="email" type="email" bind:value={email} required autocomplete="username" />
		</Field>
		<Field label="Password" id="password">
			<input
				class="input w-full"
				id="password"
				type="password"
				bind:value={password}
				required
				autocomplete="current-password"
			/>
		</Field>
		</fieldset>
		{#if error}
			<p class="alert alert-error alert-soft" role="alert">{error}</p>
		{/if}
		<Button type="submit" busy={submitting} disabled={magicSubmitting}>Sign in</Button>
		{#if sso}
			<div class="divider my-0">or</div>
			<a class="btn btn-outline" href={oidcStartUrl()}>Sign in with SSO</a>
		{/if}
	</form>

	<form class="card-body card bg-base-100 shadow" onsubmit={submitMagic}>
		<h2 class="card-title">Tenant or client?</h2>
		{#if magicSent}
			<p role="status" class="alert alert-success alert-soft">If that's a shigoto account, check your inbox for a sign-in link.</p>
		{:else}
			<fieldset disabled={magicSubmitting || submitting} class="grid min-w-0 gap-4">
			<Field label="Your email" id="magic-email">
				<input
					class="input w-full"
					id="magic-email"
					type="email"
					bind:value={magicEmail}
					required
					autocomplete="username"
				/>
			</Field>
			</fieldset>
			{#if magicError}
				<p class="alert alert-error alert-soft" role="alert">{magicError}</p>
			{/if}
			<Button type="submit" variant="secondary" busy={magicSubmitting} disabled={submitting}>
				Email me a sign-in link
			</Button>
		{/if}
	</form>
	</div>
</div>

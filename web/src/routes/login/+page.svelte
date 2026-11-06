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
		{#if sso}
			<p class="sso"><a href={oidcStartUrl()}>Sign in with SSO</a></p>
		{/if}
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
	.sso {
		margin: 0;
		text-align: center;
	}
</style>

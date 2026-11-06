<script lang="ts">
	// ReloginDialog is api.ts's default answer to a 401 that happens away
	// from /login: rather than bouncing the user off whatever they were
	// doing, it asks them to sign back in without leaving the page, then
	// api() retries the request that 401'd. Cancelling it falls back to the
	// old full redirect (api.ts's job, not this component's).
	//
	// Password-based only for now (Plan 6 Task 4 adds an SSO option; Task 6
	// changes /portal to redirect instead of showing this).
	import Dialog from './Dialog.svelte';
	import Field from './Field.svelte';
	import Button from './Button.svelte';

	let {
		open = $bindable(false),
		email,
		onSuccess,
		onCancel
	}: {
		open?: boolean;
		// Pre-filled from the current session store, when one is available -
		// this dialog never lets the user sign in as someone else.
		email: string;
		onSuccess: () => void;
		onCancel: () => void;
	} = $props();

	let password = $state('');
	let error = $state('');
	let submitting = $state(false);

	// Re-seed on every open, so a previous attempt's error or password
	// doesn't linger into the next time the session expires.
	$effect(() => {
		if (!open) return;
		password = '';
		error = '';
	});

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		submitting = true;
		try {
			// A plain fetch, not api(): api() would send a second 401 from a
			// wrong password back through onUnauthorized, trying to reopen
			// this very dialog.
			const res = await fetch('/api/v1/auth/login', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json', 'X-Shigoto-CSRF': '1' },
				body: JSON.stringify({ email, password }),
				credentials: 'same-origin'
			});
			if (!res.ok) {
				let message = res.statusText;
				try {
					const data = await res.clone().json();
					if (data && typeof data.error === 'string') {
						message = data.error;
					}
				} catch {
					// Body wasn't JSON (or was empty): fall back to statusText.
				}
				error = message;
				return;
			}
			onSuccess();
		} catch {
			error = 'Something went wrong.';
		} finally {
			submitting = false;
		}
	}
</script>

<Dialog bind:open title="Session expired" onclose={onCancel}>
	<!-- Only rendered while open: a closed native <dialog> is hidden from the
	     page visually, but its contents still exist in the DOM, so an
	     always-rendered "Email" field here would collide with the page's own
	     (e.g. the login form's) once more than one is ever mounted at once. -->
	{#if open}
		<p>Your session has expired. Sign in again to continue.</p>
		<form onsubmit={submit}>
			<Field label="Email" id="relogin-email">
				<input id="relogin-email" type="email" value={email} readonly autocomplete="username" />
			</Field>
			<Field label="Password" id="relogin-password">
				<input
					id="relogin-password"
					type="password"
					bind:value={password}
					required
					autocomplete="current-password"
				/>
			</Field>
			{#if error}
				<p class="dialog-error" role="alert">{error}</p>
			{/if}
			<div class="actions">
				<Button variant="secondary" type="button" disabled={submitting} onclick={onCancel}>
					Cancel
				</Button>
				<Button type="submit" disabled={submitting}>Sign in</Button>
			</div>
		</form>
	{/if}
</Dialog>

<style>
	form {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-3);
	}
	.dialog-error {
		margin: 0;
		padding: var(--space-2) var(--space-3);
		border-radius: var(--radius-md);
		background: var(--danger-subtle);
		color: var(--danger);
	}
</style>

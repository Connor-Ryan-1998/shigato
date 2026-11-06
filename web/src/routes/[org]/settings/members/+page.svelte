<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError } from '../../../../lib/api';
	import PageHeader from '../../../../lib/components/PageHeader.svelte';
	import Button from '../../../../lib/components/Button.svelte';
	import EmptyState from '../../../../lib/components/EmptyState.svelte';
	import MembersTable, {
		type Member
	} from '../../../../lib/modules/core/members/MembersTable.svelte';
	import InviteDialog, {
		type NewInvite
	} from '../../../../lib/modules/core/members/InviteDialog.svelte';

	type Invite = { id: number; email: string; role: string; expires_at: string };
	type CreateInviteResponse = { invite_id: number; link: string };

	let loadStatus = $state<'loading' | 'error' | 'ready'>('loading');
	let errorMessage = $state('');
	let members = $state<Member[]>([]);
	let invites = $state<Invite[]>([]);

	let busyUserId = $state<number | null>(null);
	let memberError = $state('');

	let inviteOpen = $state(false);
	let inviteBusy = $state(false);
	let inviteError = $state('');
	let inviteLink = $state('');

	async function load() {
		loadStatus = 'loading';
		errorMessage = '';
		try {
			const org = page.params.org;
			const [m, i] = await Promise.all([
				api<Member[]>('GET', `/api/v1/orgs/${org}/members`),
				api<Invite[]>('GET', `/api/v1/orgs/${org}/invites`)
			]);
			members = m;
			invites = i;
			loadStatus = 'ready';
		} catch (e) {
			errorMessage = e instanceof ApiError ? e.message : 'Something went wrong.';
			loadStatus = 'error';
		}
	}

	$effect(() => {
		page.params.org;
		load();
	});

	async function changeRole(userId: number, role: string) {
		const org = page.params.org;
		const previous = members.find((m) => m.user_id === userId)?.role;
		memberError = '';
		busyUserId = userId;
		try {
			await api('PUT', `/api/v1/orgs/${org}/members/${userId}`, { role });
			members = members.map((m) => (m.user_id === userId ? { ...m, role } : m));
		} catch (e) {
			memberError = e instanceof ApiError ? e.message : 'Something went wrong.';
			// Revert the select back to its previous value - the PUT failed (e.g.
			// the 409 for demoting the last owner), so the UI must not show a
			// role change that never actually happened server-side.
			if (previous) {
				members = members.map((m) => (m.user_id === userId ? { ...m, role: previous } : m));
			}
		} finally {
			busyUserId = null;
		}
	}

	async function removeMember(userId: number) {
		const org = page.params.org;
		memberError = '';
		busyUserId = userId;
		try {
			await api('DELETE', `/api/v1/orgs/${org}/members/${userId}`);
			members = members.filter((m) => m.user_id !== userId);
		} catch (e) {
			memberError = e instanceof ApiError ? e.message : 'Something went wrong.';
		} finally {
			busyUserId = null;
		}
	}

	async function createInvite(invite: NewInvite) {
		const org = page.params.org;
		inviteError = '';
		inviteBusy = true;
		try {
			const res = await api<CreateInviteResponse>('POST', `/api/v1/orgs/${org}/invites`, invite);
			inviteLink = res.link;
			await load();
		} catch (e) {
			inviteError = e instanceof ApiError ? e.message : 'Something went wrong.';
		} finally {
			inviteBusy = false;
		}
	}

	async function revokeInvite(id: number) {
		const org = page.params.org;
		try {
			await api('DELETE', `/api/v1/orgs/${org}/invites/${id}`);
			invites = invites.filter((i) => i.id !== id);
		} catch (e) {
			errorMessage = e instanceof ApiError ? e.message : 'Something went wrong.';
		}
	}

	function openInviteDialog() {
		inviteError = '';
		inviteLink = '';
		inviteOpen = true;
	}
</script>

<PageHeader title="Members">
	{#snippet actions()}
		<Button onclick={openInviteDialog}>Invite staff member</Button>
	{/snippet}
</PageHeader>

{#if loadStatus === 'loading'}
	<p aria-live="polite">Loading members…</p>
{:else if loadStatus === 'error'}
	<EmptyState title="Couldn't load members" description={errorMessage}>
		{#snippet action()}
			<Button onclick={load}>Retry</Button>
		{/snippet}
	</EmptyState>
{:else}
	{#if memberError}
		<p class="inline-error" role="alert">{memberError}</p>
	{/if}
	{#if members.length === 0}
		<EmptyState title="No members yet" />
	{:else}
		<MembersTable {members} {busyUserId} onRoleChange={changeRole} onRemove={removeMember} />
	{/if}

	<h2>Outstanding invites</h2>
	{#if invites.length === 0}
		<EmptyState title="No outstanding invites" />
	{:else}
		<ul class="invite-list">
			{#each invites as invite (invite.id)}
				<li>
					<span>{invite.email} — {invite.role}</span>
					<Button variant="secondary" onclick={() => revokeInvite(invite.id)}>Revoke</Button>
				</li>
			{/each}
		</ul>
	{/if}
{/if}

<InviteDialog
	bind:open={inviteOpen}
	error={inviteError}
	busy={inviteBusy}
	link={inviteLink}
	onSubmit={createInvite}
/>

<style>
	h2 {
		margin-top: var(--space-6);
		font-size: var(--text-lg);
	}
	.inline-error {
		margin: 0 0 var(--space-4);
		padding: var(--space-2) var(--space-3);
		border-radius: var(--radius-md);
		background: var(--danger-subtle);
		color: var(--danger);
	}
	.invite-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
	}
	.invite-list li {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--border);
		border-radius: var(--radius-md);
	}
</style>

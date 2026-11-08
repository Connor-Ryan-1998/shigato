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
	import PortalInviteDialog, {
		type PortalContact,
		type PortalInviteResult
	} from '../../../../lib/modules/core/members/PortalInviteDialog.svelte';

	type Invite = { id: number; email: string; role: string; expires_at: string };
	type CreateInviteResponse = { invite_id: number; link: string; emailed: boolean };
	type Contact = { id: number; name: string; kind: string };

	let loadStatus = $state<'loading' | 'error' | 'ready'>('loading');
	let errorMessage = $state('');
	let members = $state<Member[]>([]);
	let invites = $state<Invite[]>([]);

	// Bumped on every load() call; a response is only applied if it's still
	// the most recent request by the time it resolves - otherwise a slow
	// earlier load (e.g. one overtaken by a quick second refresh, or an org
	// param change) could land after a faster later one and clobber it with
	// stale data.
	let requestSeq = 0;
	// True only once the list has rendered at least once. Every later load()
	// (a refresh after an invite/role/remove/revoke action) then becomes a
	// background refresh - refreshing stays true while the already-rendered
	// table stays mounted, instead of tearing the whole view down to a bare
	// "Loading…" line for every single action.
	let hasLoadedOnce = false;
	let refreshing = $state(false);

	let busyUserId = $state<number | null>(null);
	let memberError = $state('');

	let inviteOpen = $state(false);
	let inviteBusy = $state(false);
	let inviteError = $state('');
	let inviteLink = $state('');
	let inviteEmailed = $state(false);
	let invitedEmail = $state('');

	let portalInviteOpen = $state(false);
	let portalInviteBusy = $state(false);
	let portalInviteError = $state('');
	let portalInviteResult = $state<PortalInviteResult | null>(null);
	let portalContacts = $state<PortalContact[]>([]);

	async function load() {
		const seq = ++requestSeq;
		if (hasLoadedOnce) {
			refreshing = true;
		} else {
			loadStatus = 'loading';
		}
		errorMessage = '';
		try {
			const org = page.params.org;
			const [m, i] = await Promise.all([
				api<Member[]>('GET', `/api/v1/orgs/${org}/members`),
				api<Invite[]>('GET', `/api/v1/orgs/${org}/invites`)
			]);
			if (seq !== requestSeq) return;
			members = m;
			invites = i;
			loadStatus = 'ready';
			hasLoadedOnce = true;
		} catch (e) {
			if (seq !== requestSeq) return;
			errorMessage = e instanceof ApiError ? e.message : 'Something went wrong.';
			// Once the page has rendered once, a failed refresh shows inline
			// above the still-mounted table instead of replacing it with the
			// full-page error state, which would otherwise discard the admin's
			// place (open selects, scroll position, in-flight dialog).
			if (!hasLoadedOnce) {
				loadStatus = 'error';
			}
		} finally {
			refreshing = false;
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
			await load();
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
			await load();
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
			inviteEmailed = res.emailed;
			invitedEmail = invite.email;
			await load();
		} catch (e) {
			inviteError = e instanceof ApiError ? e.message : 'Something went wrong.';
		} finally {
			inviteBusy = false;
		}
	}

	async function revokeInvite(id: number) {
		const org = page.params.org;
		memberError = '';
		try {
			await api('DELETE', `/api/v1/orgs/${org}/invites/${id}`);
			await load();
		} catch (e) {
			// Shown in the same inline slot as a failed role change/removal,
			// not the full-page load-error state: the invite list is still
			// valid and mounted, only this one revoke failed.
			memberError = e instanceof ApiError ? e.message : 'Something went wrong.';
		}
	}

	function openInviteDialog() {
		inviteError = '';
		inviteLink = '';
		inviteEmailed = false;
		invitedEmail = '';
		inviteOpen = true;
	}

	async function openPortalInviteDialog() {
		portalInviteError = '';
		portalInviteResult = null;
		portalInviteOpen = true;
		try {
			const org = page.params.org;
			const all = await api<Contact[]>('GET', `/api/v1/orgs/${org}/contacts`);
			portalContacts = all.filter((c) => c.kind === 'tenant' || c.kind === 'client');
		} catch (e) {
			portalInviteError = e instanceof ApiError ? e.message : 'Something went wrong.';
		}
	}

	async function createPortalInvite(contactId: number) {
		const org = page.params.org;
		portalInviteError = '';
		portalInviteBusy = true;
		try {
			portalInviteResult = await api<PortalInviteResult>(
				'POST',
				`/api/v1/orgs/${org}/contacts/${contactId}/portal-invite`
			);
		} catch (e) {
			portalInviteError = e instanceof ApiError ? e.message : 'Something went wrong.';
		} finally {
			portalInviteBusy = false;
		}
	}
</script>

<PageHeader title="Members">
	{#snippet actions()}
		<Button variant="secondary" onclick={openPortalInviteDialog}>Invite to portal</Button>
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
	{#if refreshing}
		<p class="mb-3 text-sm text-base-content/70" aria-live="polite">Refreshing…</p>
	{:else if errorMessage}
		<p class="alert alert-error alert-soft mb-4" role="alert">{errorMessage}</p>
	{/if}
	{#if memberError}
		<p class="alert alert-error alert-soft mb-4" role="alert">{memberError}</p>
	{/if}
	{#if members.length === 0}
		<EmptyState title="No members yet" />
	{:else}
		<MembersTable {members} {busyUserId} onRoleChange={changeRole} onRemove={removeMember} />
	{/if}

	<h2 class="mt-6 mb-3 text-lg font-semibold">Outstanding invites</h2>
	{#if invites.length === 0}
		<EmptyState title="No outstanding invites" />
	{:else}
		<ul class="list rounded-box bg-base-100 shadow-sm">
			{#each invites as invite (invite.id)}
				<li class="list-row items-center">
					<span class="list-col-grow">{invite.email} — {invite.role}</span>
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
	emailed={inviteEmailed}
	{invitedEmail}
	onSubmit={createInvite}
/>

<PortalInviteDialog
	bind:open={portalInviteOpen}
	contacts={portalContacts}
	error={portalInviteError}
	busy={portalInviteBusy}
	result={portalInviteResult}
	onSubmit={createPortalInvite}
/>

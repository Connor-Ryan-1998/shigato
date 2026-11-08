<script lang="ts" module>
	// Member mirrors GET .../members' per-row shape (internal/httpapi/members.go
	// memberResponse).
	export type Member = {
		user_id: number;
		name: string;
		email: string;
		role: string;
		contact_id?: number;
		sso_linked: boolean;
	};
</script>

<script lang="ts">
	import DataTable from '../../../components/DataTable.svelte';
	import Select from '../../../components/Select.svelte';
	import Button from '../../../components/Button.svelte';

	// Matches the roles a staff invite/member PUT accepts
	// (internal/httpapi/members.go validInviteRole).
	const ROLE_OPTIONS = [
		{ value: 'owner', label: 'Owner' },
		{ value: 'staff', label: 'Staff' },
		{ value: 'bookkeeper', label: 'Bookkeeper' }
	];

	// A tenant/client row is a portal membership, not one this page's role
	// PUT ever accepts (the server rejects it with 409 - see
	// internal/httpapi/members.go errPortalRole), so it's shown as plain
	// text rather than a select that would silently offer roles the server
	// will refuse.
	function isStaffRole(role: string): boolean {
		return role === 'owner' || role === 'staff' || role === 'bookkeeper';
	}

	let {
		members,
		busyUserId = null,
		onRoleChange,
		onRemove
	}: {
		members: Member[];
		// The member currently being updated/removed, so its row's controls
		// disable while the request is in flight rather than letting a second
		// click fire before the first one lands.
		busyUserId?: number | null;
		onRoleChange: (userId: number, role: string) => void;
		onRemove: (userId: number) => void;
	} = $props();
</script>

{#snippet roleCell(m: Member)}
	{#if isStaffRole(m.role)}
		<label class="sr-only" for={`member-role-${m.user_id}`}>Role for {m.name}</label>
		<Select
			id={`member-role-${m.user_id}`}
			value={m.role}
			disabled={busyUserId === m.user_id}
			options={ROLE_OPTIONS}
			onchange={(role) => onRoleChange(m.user_id, role)}
		/>
	{:else}
		{m.role}
	{/if}
{/snippet}

{#snippet actionsCell(m: Member)}
	<Button
		variant="secondary"
		disabled={busyUserId === m.user_id}
		onclick={() => onRemove(m.user_id)}
	>
		Remove
	</Button>
{/snippet}

<DataTable
	columns={[
		{ key: 'name', label: 'Name' },
		{ key: 'email', label: 'Email' },
		{ key: 'role', label: 'Role', cell: roleCell },
		{ key: 'actions', label: '', cell: actionsCell }
	]}
	rows={members}
	rowKey={(m) => m.user_id}
/>

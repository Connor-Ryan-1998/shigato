import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import MembersTable, { type Member } from './MembersTable.svelte';

const MEMBERS: Member[] = [
	{ user_id: 1, name: 'Ada Owner', email: 'ada@example.com', role: 'owner', sso_linked: false },
	{ user_id: 2, name: 'Bo Staff', email: 'bo@example.com', role: 'staff', sso_linked: true }
];

describe('MembersTable', () => {
	it('calls onRoleChange with the row and the new role', async () => {
		const onRoleChange = vi.fn();
		render(MembersTable, { props: { members: MEMBERS, onRoleChange, onRemove: vi.fn() } });

		const select = screen.getByLabelText('Role for Bo Staff') as HTMLSelectElement;
		await fireEvent.change(select, { target: { value: 'bookkeeper' } });

		expect(onRoleChange).toHaveBeenCalledWith(2, 'bookkeeper');
	});

	it('calls onRemove for the clicked row and disables controls while busy', () => {
		const onRemove = vi.fn();
		render(MembersTable, {
			props: { members: MEMBERS, busyUserId: 1, onRoleChange: vi.fn(), onRemove }
		});

		const removeButtons = screen.getAllByRole('button', { name: 'Remove' }) as HTMLButtonElement[];
		expect(removeButtons[0].disabled).toBe(true);
		expect(removeButtons[1].disabled).toBe(false);
	});

	it('shows a tenant/client row as plain text, not a role select', () => {
		const tenant: Member = {
			user_id: 3,
			name: 'Tess Tenant',
			email: 'tess@example.com',
			role: 'tenant',
			sso_linked: false
		};
		render(MembersTable, {
			props: { members: [tenant], onRoleChange: vi.fn(), onRemove: vi.fn() }
		});

		expect(screen.getByText('tenant')).toBeTruthy();
		expect(screen.queryByLabelText('Role for Tess Tenant')).toBeNull();
	});
});

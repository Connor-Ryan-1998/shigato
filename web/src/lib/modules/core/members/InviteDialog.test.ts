import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import InviteDialog from './InviteDialog.svelte';

describe('InviteDialog', () => {
	it('shows the link and a copy button when emailed is false', () => {
		render(InviteDialog, {
			props: { open: true, link: 'https://shigoto.example/invite/abc', emailed: false, onSubmit: vi.fn() }
		});

		expect(screen.getByLabelText('Invite link')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Copy' })).toBeTruthy();
	});

	it('shows a "sent to" note instead of the link when emailed is true', () => {
		render(InviteDialog, {
			props: {
				open: true,
				link: 'https://shigoto.example/invite/abc',
				emailed: true,
				invitedEmail: 'new@example.com',
				onSubmit: vi.fn()
			}
		});

		expect(screen.getByText(/Invite sent to/)).toBeTruthy();
		expect(screen.getByText('new@example.com')).toBeTruthy();
		expect(screen.queryByLabelText('Invite link')).toBeNull();
	});
});

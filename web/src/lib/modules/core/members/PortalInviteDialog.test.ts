import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import PortalInviteDialog from './PortalInviteDialog.svelte';

const contacts = [
	{ id: 1, name: 'Jordan Tenant', kind: 'tenant' },
	{ id: 2, name: 'Casey Client', kind: 'client' }
];

describe('PortalInviteDialog', () => {
	it('shows the link and a copy button when the invite was not emailed but a link is returned', () => {
		render(PortalInviteDialog, {
			props: {
				open: true,
				contacts,
				result: { emailed: false, link: 'https://shigoto.example/auth/magic?token=abc' },
				onSubmit: vi.fn()
			}
		});

		expect(screen.getByLabelText('Portal invite link')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Copy' })).toBeTruthy();
	});

	it('shows the Ruling 60 cross-org message with no link when the contact belongs elsewhere', () => {
		render(PortalInviteDialog, {
			props: {
				open: true,
				contacts,
				result: {
					emailed: false,
					message:
						'This person already has an account in another organisation. Configure email to send them a link, or have them request one at /login.'
				},
				onSubmit: vi.fn()
			}
		});

		expect(
			screen.getByText(
				'This person already has an account in another organisation. Configure email to send them a link, or have them request one at /login.'
			)
		).toBeTruthy();
		expect(screen.queryByLabelText('Portal invite link')).toBeNull();
	});

	it('shows a "sent" note with no link when emailed is true', () => {
		render(PortalInviteDialog, {
			props: { open: true, contacts, result: { emailed: true }, onSubmit: vi.fn() }
		});

		expect(screen.getByText('Invite sent.')).toBeTruthy();
		expect(screen.queryByLabelText('Portal invite link')).toBeNull();
	});

	// The dialog itself never knows which status (409/422/404) produced
	// `error` - the parent (members/+page.svelte) maps any ApiError message
	// onto this one prop, same as InviteDialog's own `error`. The dialog's
	// own job is only to show whatever inline message it's given, without
	// losing the form.
	it.each([
		['409 conflict', 'this email already belongs to a different member of this organisation'],
		['422 validation', 'contact must be a tenant or client'],
		['404 not found', 'not found']
	])('shows an inline error for a %s response and keeps the form', (_label, message) => {
		render(PortalInviteDialog, {
			props: { open: true, contacts, error: message, onSubmit: vi.fn() }
		});

		expect(screen.getByRole('alert').textContent).toBe(message);
		// Still on the form, not the result view - the contact picker is
		// still there to retry with.
		expect(screen.getByLabelText('Contact')).toBeTruthy();
	});

	it('submits the selected contact id', async () => {
		const onSubmit = vi.fn();
		render(PortalInviteDialog, { props: { open: true, contacts, onSubmit } });

		await fireEvent.change(screen.getByLabelText('Contact'), { target: { value: '2' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Invite' }));

		expect(onSubmit).toHaveBeenCalledWith(2);
	});
});

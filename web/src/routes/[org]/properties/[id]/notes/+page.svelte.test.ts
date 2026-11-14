import { beforeEach, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import { setPage } from '../../../../../lib/test/app.svelte';
import { apiMock, mockApi } from '../../../../../lib/test/mock-api';
import { signIn } from '../../../../../lib/test/session';
import Page from './+page.svelte';

const base = '/api/v1/orgs/acme/properties/1';

beforeEach(() => {
	setPage({ params: { org: 'acme', id: '1' } });
	signIn();
	mockApi({ [`GET ${base}`]: { id: 1, name: 'Cottage', notes: 'Gate code 1234' }, [`PUT ${base}/notes`]: (b: { notes: string }) => b });
});

it('edits and saves the notes page', async () => {
	render(Page);
	const area = (await screen.findByLabelText('Notes')) as HTMLTextAreaElement;
	expect(area.value).toBe('Gate code 1234');
	await fireEvent.input(area, { target: { value: 'Gate code 5678' } });
	await fireEvent.click(screen.getByRole('button', { name: 'Save notes' }));
	await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('PUT', `${base}/notes`, { notes: 'Gate code 5678' }));
	expect(await screen.findByText('Notes saved.')).toBeTruthy();
});

it('cancel restores the last saved notes', async () => {
	render(Page);
	const area = (await screen.findByLabelText('Notes')) as HTMLTextAreaElement;
	await fireEvent.input(area, { target: { value: 'scratch' } });
	vi.mocked(window.confirm).mockReturnValue(true);
	await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
	expect(area.value).toBe('Gate code 1234');
});

it('is read-only without write permission', async () => {
	signIn({ role: 'bookkeeper', perms: ['property_read'] });
	render(Page);
	expect((await screen.findByLabelText('Notes')).matches(':disabled')).toBe(true);
	expect(screen.queryByRole('button', { name: 'Save notes' })).toBeNull();
});

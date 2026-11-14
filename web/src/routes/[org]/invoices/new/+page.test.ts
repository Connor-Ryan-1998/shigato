import { beforeEach, describe, expect, it } from 'vitest';
import { goto, setPage } from '../../../../lib/test/app.svelte';
import { apiMock } from '../../../../lib/test/mock-api';
import { render, screen, fireEvent } from '@testing-library/svelte';

import Page from './+page.svelte';

beforeEach(() => setPage({ params: { org: 'acme' }, url: new URL('http://localhost/acme/invoices/new') }));

// The contact dialog's editor rules are in lib/test/editors/invoice-new-contact.yaml.
describe('new invoice page', () => {
	it.each(['pending', 'rejected'])('retains a saved invoice without allowing another write when navigation is %s', async (navigation) => {
		apiMock.mockImplementation((method: string) => Promise.resolve(method === 'GET' ? [{ id: 4, name: 'Client', kind: 'client' }] : { id: 77 }));
		goto.mockImplementation(() => navigation === 'pending' ? new Promise(() => {}) : Promise.reject(new Error('Navigation failed')));
		render(Page);
		await screen.findByRole('option', { name: 'Client' });
		await fireEvent.input(screen.getByLabelText('Description'), { target: { value: 'Work' } });
		await fireEvent.input(screen.getByLabelText('Quantity'), { target: { value: '1' } });
		await fireEvent.input(screen.getByLabelText('Unit price ($)'), { target: { value: '100' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
		expect((await screen.findByRole('link', { name: 'View saved invoice' })).getAttribute('href')).toBe('/acme/invoices/77');
		expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
		expect(apiMock.mock.calls.filter(([method]) => method === 'POST')).toHaveLength(1);
	});
});

import { beforeEach, expect, it } from 'vitest';
import { createRawSnippet } from 'svelte';
import { render, screen } from '@testing-library/svelte';
import { setPage } from '../../../../lib/test/app.svelte';
import { mockApi } from '../../../../lib/test/mock-api';
import { signIn } from '../../../../lib/test/session';
import Layout from './+layout.svelte';

const children = createRawSnippet(() => ({ render: () => '<p>Tab body</p>' }));

beforeEach(() => {
	signIn();
	mockApi({ 'GET /api/v1/orgs/acme/properties/1': { id: 1, name: 'Cottage' } });
});

it('titles the page with the property and marks the active section', async () => {
	setPage({ params: { org: 'acme', id: '1' }, url: '/acme/properties/1/bills' });
	render(Layout, { children });
	expect(await screen.findByRole('heading', { name: 'Cottage' })).toBeTruthy();
	const nav = screen.getByRole('navigation', { name: 'Property sections' });
	const links = [...nav.querySelectorAll('a')].map((a) => [a.textContent, a.getAttribute('href'), a.getAttribute('aria-current')]);
	expect(links).toEqual([
		['Details', '/acme/properties/1', null],
		['Bills', '/acme/properties/1/bills', 'page'],
		['Depreciation', '/acme/properties/1/depreciation', null],
		['Notes', '/acme/properties/1/notes', null]
	]);
	expect(screen.getByRole('link', { name: 'Back to properties' }).getAttribute('href')).toBe('/acme/properties');
	expect(screen.getByText('Tab body')).toBeTruthy();
});

import { describe, expect, it, vi } from 'vitest';
import { createRawSnippet } from 'svelte';
import { fireEvent, render, screen } from '@testing-library/svelte';
import Button from './Button.svelte';
import PageHeader from './PageHeader.svelte';
import DataTable from './DataTable.svelte';
import Toast from './Toast.svelte';
import Field from './Field.svelte';

const text = createRawSnippet(() => ({ render: () => '<span>Save</span>' }));

describe('platform contracts', () => {
	it('associates field errors with their control', () => {
		const children = createRawSnippet(() => ({ render: () => '<input id="rate" />' }));
		render(Field, { id: 'rate', label: 'Rate', error: 'Invalid amount', children });
		const input = screen.getByLabelText('Rate');
		expect(input.getAttribute('aria-describedby')).toBe('rate-error');
		expect(input.getAttribute('aria-invalid')).toBe('true');
	});
	it('blocks actions while busy', async () => {
		const onclick = vi.fn();
		const props = { children: text, busy: true, onclick };
		render(Button, props);
		const button = screen.getByRole('button', { name: 'Save' });
		expect(button).toHaveProperty('disabled', true);
		expect(button.getAttribute('aria-busy')).toBe('true');
		await fireEvent.click(button);
		expect(onclick).not.toHaveBeenCalled();
	});
	it('provides back navigation without a second heading', () => {
		const props = { title: 'Project', back: { href: '/acme/projects', label: 'Back to projects' } };
		render(PageHeader, props);
		expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1);
		expect(screen.getByRole('link', { name: 'Back to projects' }).getAttribute('href')).toBe('/acme/projects');
	});
	it('names tables and aligns numeric columns', () => {
		const props = { caption: 'Hours', columns: [{ key: 'hours', label: 'Hours', align: 'right' as const }], rows: [{ id: 1, hours: 12 }], rowKey: (row: unknown) => (row as {id: number}).id };
		render(DataTable, props);
		expect(screen.getByRole('table', { name: 'Hours' })).toBeTruthy();
		expect(screen.getByRole('cell', { name: '12' }).classList.contains('text-right')).toBe(true);
	});
	it('announces errors as alerts', () => {
		render(Toast, { message: 'Save failed', tone: 'danger' });
		expect(screen.getByRole('alert').textContent).toContain('Save failed');
	});
});

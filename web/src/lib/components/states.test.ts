import { expect, it, vi } from 'vitest';
import { createRawSnippet } from 'svelte';
import { fireEvent, render, screen } from '@testing-library/svelte';
import Pagination from './Pagination.svelte';
import LoadState from './LoadState.svelte';
import { canDismiss } from '../forms/dismiss';

const children = createRawSnippet(() => ({ render: () => '<p>Records</p>' }));

it('paginates and recovers from an out-of-range page', async () => {
	const onchange = vi.fn();
	render(Pagination, { offset: 100, count: 0, total: 53, pageSize: 50, onchange });
	expect(screen.getByText('0 of 53')).toBeTruthy();
	expect(screen.getByRole('button', { name: 'Next' })).toHaveProperty('disabled', true);
	await fireEvent.click(screen.getByRole('button', { name: 'Previous' }));
	expect(onchange).toHaveBeenCalledWith(50);
});

it('offers retry rather than an empty state on failure', async () => {
	const onretry = vi.fn();
	render(LoadState, { loading: false, error: 'Offline', empty: true, loadingLabel: 'Loading records…', onretry, children });
	expect(screen.getByRole('alert').textContent).toContain('Offline');
	expect(screen.queryByText('Records')).toBeNull();
	await fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
	expect(onretry).toHaveBeenCalledOnce();
});

it('does not prompt for clean editors and refuses busy dismissal', () => {
	const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
	expect(canDismiss(false, false)).toBe(true);
	expect(canDismiss(true, false)).toBe(false);
	expect(confirm).not.toHaveBeenCalled();
	expect(canDismiss(false, true)).toBe(false);
	expect(confirm).toHaveBeenCalledWith('Discard unsaved changes?');
	confirm.mockReturnValue(true);
	expect(canDismiss(false, true)).toBe(true);
	confirm.mockRestore();
});

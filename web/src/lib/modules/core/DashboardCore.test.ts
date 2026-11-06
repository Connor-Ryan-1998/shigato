import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import DashboardCore from './DashboardCore.svelte';

const core = {
	open_count: 3,
	open_outstanding: 123456,
	overdue_count: 1,
	overdue_outstanding: 7890,
	draft_count: 2,
	invoice_count: 7
};

describe('DashboardCore', () => {
	it('formats outstanding amounts as currency, never computed client-side', () => {
		render(DashboardCore, { core });
		expect(screen.getByText('$1,234.56')).toBeTruthy();
		expect(screen.getByText('$78.90')).toBeTruthy();
	});

	it('labels the overdue tile with text, not colour alone', () => {
		render(DashboardCore, { core });
		expect(screen.getByText('Overdue')).toBeTruthy();
	});

	it('shows the draft count', () => {
		render(DashboardCore, { core });
		expect(screen.getByText('2')).toBeTruthy();
	});

	it('links tiles to the invoice list filters the Invoices screen expects', () => {
		render(DashboardCore, { core });
		const openLink = screen.getByRole('link', { name: /open invoices/i });
		const overdueLink = screen.getByRole('link', { name: /overdue/i });
		expect(openLink.getAttribute('href')).toBe('invoices?status=open');
		expect(overdueLink.getAttribute('href')).toBe('invoices?status=overdue');
	});
});

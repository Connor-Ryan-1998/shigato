import { expect, it } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import DashboardCard from './DashboardCard.svelte';

it('shows how many insurance policies renew soon', () => {
	render(DashboardCard, { data: { arrears_total: 0, unissued_total: 0, bonds_unlodged_overdue: 0, insurance_renewing_soon: 2 } });
	const term = screen.getByText('Insurance renewing within 30 days');
	expect(term.nextElementSibling?.textContent).toBe('2');
});

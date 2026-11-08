import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import HoursChart from './HoursChart.svelte';
import type { Entry } from './types';

const entry = (work_date: string, minutes: number, extra: Partial<Entry> = {}) =>
	({ id: Math.random(), work_date, minutes, billing_method: 'tm', ...extra }) as Entry;

describe('HoursChart', () => {
	it('buckets this week by day and billing status', () => {
		// 2026-11-08 is a Sunday: week is Mon 2 - Sun 8 Nov.
		render(HoursChart, {
			today: '2026-11-08',
			entries: [
				entry('2026-11-02', 60, { invoice_line_id: 1 }),
				entry('2026-11-02', 30),
				entry('2026-11-08', 45, { billing_method: 'fixed' }),
				entry('2026-11-01', 600) // previous week: ignored
			]
		});
		const items = screen.getAllByRole('listitem').map((li) => li.textContent);
		expect(items).toHaveLength(7);
		expect(items[0]).toContain('01:00 billed, 00:30 unbilled, 00:00 fixed fee');
		expect(items[6]).toContain('00:00 billed, 00:00 unbilled, 00:45 fixed fee');
		expect(items.slice(1, 6).every((t) => t?.includes('00:00 billed, 00:00 unbilled, 00:00 fixed fee'))).toBe(true);
	});
});

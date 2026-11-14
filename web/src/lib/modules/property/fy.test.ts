import { expect, it } from 'vitest';
import { currentFY, fyLabel, fyOptions } from './fy';

it('names the financial year by its end year (July start)', () => {
	expect(currentFY('2026-06-30')).toBe(2026);
	expect(currentFY('2026-07-01')).toBe(2027);
	expect(currentFY('2027-01-15')).toBe(2027);
});

it('labels and lists recent financial years, newest first', () => {
	expect(fyLabel(2027)).toBe('FY2026–27');
	expect(fyOptions(2027, 2)).toEqual([
		{ value: '2027', label: 'FY2026–27' },
		{ value: '2026', label: 'FY2025–26' },
		{ value: '2025', label: 'FY2024–25' }
	]);
});

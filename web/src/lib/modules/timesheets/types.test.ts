import { describe, expect, it } from 'vitest';
import { billingStatus, entryValue, hhmm, parseDuration, periodRange } from './types';

describe('timesheet helpers', () => {
	it('formats minutes as HH:MM', () => {
		expect(hhmm(0)).toBe('00:00');
		expect(hhmm(165)).toBe('02:45');
		expect(hhmm(3345)).toBe('55:45');
	});

	it('values an entry at its rate', () => {
		expect(entryValue({ minutes: 50, rate: 12000 })).toBe(10000);
		expect(entryValue({ minutes: 1, rate: 10000 })).toBe(167);
	});

	it('derives billing status', () => {
		expect(billingStatus({ invoice_line_id: 3, billing_method: 'fixed' })).toBe('billed');
		expect(billingStatus({ billing_method: 'tm' })).toBe('unbilled');
		expect(billingStatus({ billing_method: 'fixed' })).toBe('fixed');
	});

	it('computes period ranges', () => {
		// 2026-11-08 is a Sunday.
		expect(periodRange('all', '2026-11-08')).toBeNull();
		expect(periodRange('this_week', '2026-11-08')).toEqual({ from: '2026-11-02', to: '2026-11-08' });
		expect(periodRange('this_week', '2026-11-02')).toEqual({ from: '2026-11-02', to: '2026-11-08' });
		expect(periodRange('this_month', '2026-02-10')).toEqual({ from: '2026-02-01', to: '2026-02-28' });
		expect(periodRange('last_month', '2026-01-15')).toEqual({ from: '2025-12-01', to: '2025-12-31' });
	});

	it('parses durations', () => {
		expect(parseDuration('2:45')).toBe(165);
		expect(parseDuration('90')).toBe(90);
		expect(parseDuration('0:00')).toBeNull();
		expect(parseDuration('25:00')).toBeNull();
		expect(parseDuration('1:75')).toBeNull();
		expect(parseDuration('abc')).toBeNull();
	});
});

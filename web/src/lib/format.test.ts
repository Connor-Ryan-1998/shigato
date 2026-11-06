import { describe, expect, it } from 'vitest';
import { date, minutes, money } from './format';

describe('money', () => {
	it('formats positive cents as AUD', () => {
		expect(money(123456)).toBe('$1,234.56');
	});

	it('formats negative cents as AUD', () => {
		expect(money(-500)).toBe('-$5.00');
	});
});

describe('date', () => {
	it('formats an ISO date as en-AU', () => {
		expect(date('2026-11-05')).toBe('5 Nov 2026');
	});
});

describe('minutes', () => {
	it('formats minutes as hours and minutes', () => {
		expect(minutes(125)).toBe('2h 05m');
	});
});

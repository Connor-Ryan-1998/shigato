import { describe, expect, it } from 'vitest';
import { date, minutes, money, parseCents, parseQuantityMilli } from './format';

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

describe('parseCents', () => {
	it('parses a one-decimal amount', () => {
		expect(parseCents('12.3')).toBe(1230);
	});

	it('parses a comma-grouped amount with two decimals', () => {
		expect(parseCents('1,200.50')).toBe(120050);
	});

	it('parses a whole-dollar amount', () => {
		expect(parseCents('12')).toBe(1200);
	});

	it('rejects more than two decimal places', () => {
		expect(parseCents('1.234')).toBeNull();
	});

	it('rejects an empty string', () => {
		expect(parseCents('')).toBeNull();
	});

	it('rejects a negative amount', () => {
		expect(parseCents('-5.00')).toBeNull();
	});

	it('rejects a non-numeric string', () => {
		expect(parseCents('abc')).toBeNull();
	});
});

describe('parseQuantityMilli', () => {
	it('parses a one-decimal quantity', () => {
		expect(parseQuantityMilli('1.5')).toBe(1500);
	});

	it('parses a whole quantity', () => {
		expect(parseQuantityMilli('3')).toBe(3000);
	});

	it('rejects more than three decimal places', () => {
		expect(parseQuantityMilli('0.0005')).toBeNull();
	});

	it('rejects an empty string', () => {
		expect(parseQuantityMilli('')).toBeNull();
	});

	it('rejects a negative quantity', () => {
		expect(parseQuantityMilli('-1.5')).toBeNull();
	});

	it('rejects a non-numeric string', () => {
		expect(parseQuantityMilli('abc')).toBeNull();
	});
});

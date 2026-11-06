import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { date, minutes, money, parseCents, parseQuantityMilli, todayLocal } from './format';

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

	it('rejects internal spaces', () => {
		expect(parseCents('1 200')).toBeNull();
	});

	it('rejects malformed thousands grouping (groups not 3 digits)', () => {
		expect(parseCents('1,2,3')).toBeNull();
	});

	it('rejects a leading-zero group before a comma', () => {
		expect(parseCents('0,100')).toBeNull();
	});

	it('rejects a decimal point with no leading digit', () => {
		expect(parseCents('.5')).toBeNull();
	});

	it('rejects a decimal point with no digits after it', () => {
		expect(parseCents('1.')).toBeNull();
	});

	it('rejects "-0"', () => {
		expect(parseCents('-0')).toBeNull();
	});

	it('rejects a value over the server bound (100,000,000,000 cents)', () => {
		expect(parseCents('2000000000.00')).toBeNull();
	});

	it('accepts exactly the server bound', () => {
		expect(parseCents('1000000000.00')).toBe(100_000_000_000);
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

	it('rejects internal spaces', () => {
		expect(parseQuantityMilli('1 5')).toBeNull();
	});

	it('rejects a decimal point with no leading digit', () => {
		expect(parseQuantityMilli('.5')).toBeNull();
	});

	it('rejects a decimal point with no digits after it', () => {
		expect(parseQuantityMilli('1.')).toBeNull();
	});

	it('rejects "-0"', () => {
		expect(parseQuantityMilli('-0')).toBeNull();
	});

	it('rejects a value over the server bound (10,000,000 milli)', () => {
		expect(parseQuantityMilli('20000.5')).toBeNull();
	});

	it('accepts exactly the server bound', () => {
		expect(parseQuantityMilli('10000')).toBe(10_000_000);
	});
});

describe('todayLocal', () => {
	const originalTZ = process.env.TZ;

	beforeEach(() => {
		// Brisbane is UTC+10 with no daylight saving, so this is a fixed,
		// deterministic offset regardless of the host machine's own
		// timezone - the point of the test.
		process.env.TZ = 'Australia/Brisbane';
	});

	afterEach(() => {
		vi.useRealTimers();
		process.env.TZ = originalTZ;
	});

	it('uses the local calendar date, not UTC, for a late-UTC/early-local instant', () => {
		// 23:30 UTC on the 6th is already 09:30 on the 7th in Brisbane.
		vi.setSystemTime(new Date('2026-11-06T23:30:00Z'));
		expect(todayLocal()).toBe('2026-11-07');
	});
});

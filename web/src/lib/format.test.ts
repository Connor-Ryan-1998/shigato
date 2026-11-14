import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { date, minutes, money, parseCents, parseQuantityMilli, todayLocal } from './format';
import { loadTable } from './test/table';
import source from './testdata/format.yaml?raw';

type Case = { name: string; fn: keyof typeof fns; in: string | number; want: string | number | null };
const fns = { money, date, minutes, parseCents, parseQuantityMilli } as const;
const file = 'lib/testdata/format.yaml';

describe(file, () => {
	for (const c of loadTable<Case>(source, file, ['name', 'fn', 'in', 'want'])) {
		it(c.name, () => {
			const fn = fns[c.fn] as (input: string | number) => unknown;
			if (!fn) throw new Error(`${file}: case "${c.name}": unknown fn "${c.fn}"`);
			expect(fn(c.in)).toBe(c.want);
		});
	}
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

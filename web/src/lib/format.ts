// Money arrives from the API as integer cents and is only ever formatted
// here for display - never computed client-side. Dates arrive as
// YYYY-MM-DD strings and are parsed as UTC calendar dates so the formatted
// day never shifts with the viewer's local timezone.

const currencyFormatter = new Intl.NumberFormat('en-AU', {
	style: 'currency',
	currency: 'AUD'
});

export function money(cents: number): string {
	return currencyFormatter.format(cents / 100);
}

const dateFormatter = new Intl.DateTimeFormat('en-AU', {
	day: 'numeric',
	month: 'short',
	year: 'numeric',
	timeZone: 'UTC'
});

export function date(iso: string): string {
	const [year, month, day] = iso.split('-').map(Number);
	return dateFormatter.format(new Date(Date.UTC(year, month - 1, day)));
}

// MAX_UNIT_AMOUNT_CENTS and MAX_QUANTITY_MILLI mirror the server's own
// bounds (internal/core/invoices.go CreateDraftInvoice): unit_amount from 0
// to 100,000,000,000 cents, quantity_milli from 1 to 10,000,000. Rejecting
// out-of-range input here gives an inline error instead of a round trip to
// the server for a value that can never be accepted.
const MAX_UNIT_AMOUNT_CENTS = 100_000_000_000;
const MAX_QUANTITY_MILLI = 10_000_000;

// validGrouping checks a non-negative integer string (no sign, no decimal
// point) for a plausible shape: either no thousands separators at all (and
// no leading zero, since "0123" isn't a number anyone would type), or
// comma-grouped with a 1-3 digit leading group and exactly-3-digit
// following groups ("1,200", not "0,100" or "1,2,3").
function validGrouping(whole: string): boolean {
	if (whole === '') return false;
	if (!whole.includes(',')) {
		return whole === '0' || /^[1-9]\d*$/.test(whole);
	}
	const groups = whole.split(',');
	if (!/^[1-9]\d{0,2}$/.test(groups[0])) return false;
	return groups.slice(1).every((g) => /^\d{3}$/.test(g));
}

// parseCents parses a dollar-amount string (optionally with thousands
// commas and up to 2 decimal places) into integer cents, by splitting the
// string on '.' and doing integer maths on the two halves - never
// parseFloat, which would round a value like "0.1" to an imprecise binary
// float before it's even multiplied. Returns null for anything that isn't a
// plain, in-range, non-negative amount: empty, negative, containing spaces,
// non-numeric, malformed thousands grouping, more than 2 decimal places, a
// decimal point with no digits after it (or none before it), or over the
// server's own bound.
export function parseCents(input: string): number | null {
	const s = input.trim();
	if (!/^[\d,.]+$/.test(s)) return null;
	const parts = s.split('.');
	if (parts.length > 2) return null;
	const [whole, frac] = parts;
	if (frac !== undefined && !/^\d{1,2}$/.test(frac)) return null;
	if (!validGrouping(whole)) return null;

	const cents = Number(whole.replace(/,/g, '')) * 100 + Number((frac ?? '').padEnd(2, '0'));
	if (!Number.isSafeInteger(cents) || cents < 0 || cents > MAX_UNIT_AMOUNT_CENTS) return null;
	return cents;
}

// parseQuantityMilli parses a decimal quantity (hours or units, no
// thousands separators - quantities aren't grouped) into thousandths
// (milli-units), the same way: string split, integer maths, no parseFloat.
// Returns null for empty, negative, containing spaces, non-numeric, a
// leading zero like "0123", more than 3 decimal places, a bare decimal
// point, or outside the server's 1..10,000,000 bound.
export function parseQuantityMilli(input: string): number | null {
	const s = input.trim();
	if (!/^[\d.]+$/.test(s)) return null;
	const parts = s.split('.');
	if (parts.length > 2) return null;
	const [whole, frac] = parts;
	if (frac !== undefined && !/^\d{1,3}$/.test(frac)) return null;
	if (whole === '' || (whole !== '0' && !/^[1-9]\d*$/.test(whole))) return null;

	const milli = Number(whole) * 1000 + Number((frac ?? '').padEnd(3, '0'));
	if (!Number.isSafeInteger(milli) || milli < 1 || milli > MAX_QUANTITY_MILLI) return null;
	return milli;
}

// todayLocal returns the viewer's local calendar date (not UTC) as
// YYYY-MM-DD. new Date().toISOString() reads as UTC, which is the wrong
// calendar day for part of the day in any timezone ahead of UTC - e.g.
// 2026-11-06T23:30Z is already the morning of 2026-11-07 in Brisbane
// (UTC+10). Used to default date-paid inputs to "today" as the user who's
// looking at the screen understands it.
export function todayLocal(): string {
	const now = new Date();
	const year = now.getFullYear();
	const month = String(now.getMonth() + 1).padStart(2, '0');
	const day = String(now.getDate()).padStart(2, '0');
	return `${year}-${month}-${day}`;
}

export function minutes(m: number): string {
	const sign = m < 0 ? '-' : '';
	const abs = Math.abs(m);
	const hours = Math.floor(abs / 60);
	const mins = abs % 60;
	return `${sign}${hours}h ${String(mins).padStart(2, '0')}m`;
}

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

// parseCents parses a dollar-amount string (optionally with thousands
// commas and up to 2 decimal places) into integer cents, by splitting the
// string on '.' and doing integer maths on the two halves - never
// parseFloat, which would round a value like "0.1" to an imprecise binary
// float before it's even multiplied. Returns null for anything that isn't
// a plain non-negative amount: empty, negative, non-numeric, or more than
// 2 decimal places.
export function parseCents(input: string): number | null {
	const s = input.trim();
	if (!/^(?:\d{1,3}(?:,\d{3})*|\d+)(?:\.\d{1,2})?$/.test(s)) return null;
	const [whole, frac = ''] = s.replace(/,/g, '').split('.');
	return Number(whole) * 100 + Number(frac.padEnd(2, '0'));
}

// parseQuantityMilli parses a decimal quantity (hours or units) into
// thousandths (milli-units), the same way: string split, integer maths, no
// parseFloat. Returns null for empty, negative, non-numeric, or more than
// 3 decimal places (anything finer than a milli-unit can't be represented
// exactly).
export function parseQuantityMilli(input: string): number | null {
	const s = input.trim();
	if (!/^\d+(?:\.\d{1,3})?$/.test(s)) return null;
	const [whole, frac = ''] = s.split('.');
	return Number(whole) * 1000 + Number(frac.padEnd(3, '0'));
}

export function minutes(m: number): string {
	const sign = m < 0 ? '-' : '';
	const abs = Math.abs(m);
	const hours = Math.floor(abs / 60);
	const mins = abs % 60;
	return `${sign}${hours}h ${String(mins).padStart(2, '0')}m`;
}

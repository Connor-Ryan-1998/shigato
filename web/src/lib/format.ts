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

export function minutes(m: number): string {
	const sign = m < 0 ? '-' : '';
	const abs = Math.abs(m);
	const hours = Math.floor(abs / 60);
	const mins = abs % 60;
	return `${sign}${hours}h ${String(mins).padStart(2, '0')}m`;
}

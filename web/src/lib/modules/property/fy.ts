// Australian financial years (1 July start), named by their end year as
// the API's `fy` parameter is: FY2027 runs 2026-07-01 to 2027-06-30.
// shortcut: assumes fy_start_month 7, like the Reports page.

export function currentFY(today: string): number {
	const [y, m] = today.split('-').map(Number);
	return m >= 7 ? y + 1 : y;
}

export function fyLabel(fy: number): string {
	return `FY${fy - 1}–${String(fy).slice(-2)}`;
}

export function fyOptions(current: number, back = 5): { value: string; label: string }[] {
	return Array.from({ length: back + 1 }, (_, i) => ({ value: String(current - i), label: fyLabel(current - i) }));
}

// fyFromURL reads ?fy= when it is a plausible year, else the current FY.
export function fyFromURL(url: { searchParams: { get(name: string): string | null } } | undefined, today: string): number {
	const v = Number(url?.searchParams.get('fy'));
	return Number.isInteger(v) && v >= 1900 && v <= 2200 ? v : currentFY(today);
}

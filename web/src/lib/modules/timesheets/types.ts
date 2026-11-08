// Shared shapes and helpers for the Projects module (projects, tasks and
// time entries), matching internal/timesheets/routes.go's JSON.

export type ProjectStatus = 'pending' | 'in_progress' | 'completed' | 'cancelled';
export type BillingMethod = 'tm' | 'fixed';

export type Project = {
	id: number;
	client_contact_id: number;
	name: string;
	hourly_rate: number;
	gst_code: string;
	status: ProjectStatus;
	description: string;
	start_date: string | null;
	end_date: string | null;
	billing_method: BillingMethod;
	// Derived server-side: pending or in progress.
	active: boolean;
};

export type Task = { id: number; project_id: number; name: string; hourly_rate?: number; active: boolean };

// A time entry as GET /time-entries lists it (with its details).
export type Entry = {
	id: number;
	task_id: number;
	user_id: number;
	work_date: string;
	minutes: number;
	note: string;
	invoice_line_id?: number;
	task_name: string;
	project_id: number;
	project_name: string;
	billing_method: BillingMethod;
	client_contact_id: number;
	client_name: string;
	user_name: string;
	rate: number;
};

export const STATUS_LABELS: Record<ProjectStatus, string> = {
	pending: 'Pending',
	in_progress: 'In Progress',
	completed: 'Completed',
	cancelled: 'Cancelled'
};

export const STATUS_TONES: Record<ProjectStatus, 'info' | 'success' | 'warning' | 'danger'> = {
	pending: 'info',
	in_progress: 'success',
	completed: 'warning',
	cancelled: 'danger'
};

export const BILLING_LABELS: Record<BillingMethod, string> = {
	tm: 'Time and Materials',
	fixed: 'Fixed'
};

// hhmm formats minutes as "HH:MM" (e.g. 165 -> "02:45").
export function hhmm(minutes: number): string {
	const h = Math.floor(minutes / 60);
	const m = minutes % 60;
	return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`;
}

// entryValue is an entry's value at its effective rate, in cents, rounded
// the same way the billing run's LineFor does (half away from zero).
export function entryValue(e: Pick<Entry, 'minutes' | 'rate'>): number {
	return Math.round((e.minutes * e.rate) / 60);
}

export type BillingStatus = 'billed' | 'unbilled' | 'fixed';

// billingStatus: invoiced time is billed; unbilled time on a fixed-price
// project is never billed by a billing run, so it's "fixed".
export function billingStatus(e: Pick<Entry, 'invoice_line_id' | 'billing_method'>): BillingStatus {
	if (e.invoice_line_id) return 'billed';
	return e.billing_method === 'fixed' ? 'fixed' : 'unbilled';
}

export type Period = 'all' | 'this_week' | 'this_month' | 'last_month';

export const PERIOD_LABELS: Record<Period, string> = {
	all: 'All',
	this_week: 'This week',
	this_month: 'This month',
	last_month: 'Last month'
};

function iso(d: Date): string {
	return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

// periodRange turns a period into inclusive YYYY-MM-DD bounds relative to
// today (a YYYY-MM-DD string, e.g. todayLocal()). Weeks start on Monday.
export function periodRange(period: Period, today: string): { from: string; to: string } | null {
	const [y, m, d] = today.split('-').map(Number);
	const t = new Date(y, m - 1, d);
	switch (period) {
		case 'all':
			return null;
		case 'this_week': {
			const monday = new Date(y, m - 1, d - ((t.getDay() + 6) % 7));
			const sunday = new Date(monday.getFullYear(), monday.getMonth(), monday.getDate() + 6);
			return { from: iso(monday), to: iso(sunday) };
		}
		case 'this_month':
			return { from: iso(new Date(y, m - 1, 1)), to: iso(new Date(y, m, 0)) };
		case 'last_month':
			return { from: iso(new Date(y, m - 2, 1)), to: iso(new Date(y, m - 1, 0)) };
	}
}

// parseDuration accepts "H:MM" / "HH:MM" or a plain number of minutes and
// returns minutes, or null if it isn't a valid 1-1440 minute duration.
export function parseDuration(input: string): number | null {
	const s = input.trim();
	let total: number;
	const clock = /^(\d{1,2}):([0-5]\d)$/.exec(s);
	if (clock) total = Number(clock[1]) * 60 + Number(clock[2]);
	else if (/^\d+$/.test(s)) total = Number(s);
	else return null;
	return total >= 1 && total <= 1440 ? total : null;
}

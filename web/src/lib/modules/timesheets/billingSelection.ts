// Pure selection rules for the billing-run dialog's project/task tree.
// Search only changes what is shown: selection, project toggles and totals
// always cover every previewed task, hidden or not.

export type PreviewTask = { task_id: number; task_name: string; minutes: number; amount: number; gst_amount: number };
export type PreviewProject = {
	project_id: number;
	project_name: string;
	minutes: number;
	amount: number;
	gst_amount: number;
	tasks: PreviewTask[];
};

// filterProjects keeps projects whose name matches query (with all their
// tasks) and, for other projects, only the tasks whose name matches.
// Matching is case-insensitive on the trimmed query; '' keeps everything.
export function filterProjects(projects: PreviewProject[], query: string): PreviewProject[] {
	const q = query.trim().toLowerCase();
	if (q === '') return projects;
	const out: PreviewProject[] = [];
	for (const p of projects) {
		if (p.project_name.toLowerCase().includes(q)) {
			out.push(p);
			continue;
		}
		const tasks = p.tasks.filter((t) => t.task_name.toLowerCase().includes(q));
		if (tasks.length > 0) out.push({ ...p, tasks });
	}
	return out;
}

export function projectState(p: PreviewProject, selected: ReadonlySet<number>): 'all' | 'some' | 'none' {
	const n = p.tasks.filter((t) => selected.has(t.task_id)).length;
	return n === 0 ? 'none' : n === p.tasks.length ? 'all' : 'some';
}

// toggleProject clears every task of p when all are selected, otherwise
// selects them all. p must be the full project, not a filtered view.
export function toggleProject(p: PreviewProject, selected: ReadonlySet<number>): Set<number> {
	const next = new Set(selected);
	const clear = projectState(p, selected) === 'all';
	for (const t of p.tasks) {
		if (clear) next.delete(t.task_id);
		else next.add(t.task_id);
	}
	return next;
}

function selectedTasks(projects: PreviewProject[], selected: ReadonlySet<number>): PreviewTask[] {
	return projects.flatMap((p) => p.tasks.filter((t) => selected.has(t.task_id)));
}

export function selectionTotals(projects: PreviewProject[], selected: ReadonlySet<number>): { minutes: number; amount: number; gst_amount: number } {
	const totals = { minutes: 0, amount: 0, gst_amount: 0 };
	for (const t of selectedTasks(projects, selected)) {
		totals.minutes += t.minutes;
		totals.amount += t.amount;
		totals.gst_amount += t.gst_amount;
	}
	return totals;
}

// selectionPayload is the run request's tasks: each selected task with the
// minutes and amount its preview showed, so the server can refuse a stale
// review.
export function selectionPayload(projects: PreviewProject[], selected: ReadonlySet<number>): { task_id: number; minutes: number; amount: number }[] {
	return selectedTasks(projects, selected).map((t) => ({ task_id: t.task_id, minutes: t.minutes, amount: t.amount }));
}

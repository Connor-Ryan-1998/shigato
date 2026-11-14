import { describe, expect, it } from 'vitest';
import {
	filterProjects,
	projectState,
	selectionPayload,
	selectionTotals,
	toggleProject,
	type PreviewProject
} from './billingSelection';

const PROJECTS: PreviewProject[] = [
	{
		project_id: 1,
		project_name: 'Website',
		minutes: 90,
		amount: 18000,
		gst_amount: 1800,
		tasks: [
			{ task_id: 11, task_name: 'Design', minutes: 60, amount: 12000, gst_amount: 1200 },
			{ task_id: 12, task_name: 'Build', minutes: 30, amount: 6000, gst_amount: 600 }
		]
	},
	{
		project_id: 2,
		project_name: 'Support',
		minutes: 45,
		amount: 4500,
		gst_amount: 0,
		tasks: [{ task_id: 21, task_name: 'Design review', minutes: 45, amount: 4500, gst_amount: 0 }]
	}
];

const all = () => new Set([11, 12, 21]);

describe('billing selection', () => {
	it('filter by project keeps all tasks', () => {
		const out = filterProjects(PROJECTS, '  website ');
		expect(out.map((p) => p.project_id)).toEqual([1]);
		expect(out[0].tasks.map((t) => t.task_id)).toEqual([11, 12]);
	});

	it('filter by task keeps project context', () => {
		const out = filterProjects(PROJECTS, 'design');
		expect(out.map((p) => [p.project_id, p.tasks.map((t) => t.task_id)])).toEqual([
			[1, [11]],
			[2, [21]]
		]);
	});

	it('empty query shows everything', () => {
		expect(filterProjects(PROJECTS, '')).toEqual(PROJECTS);
	});

	it('project toggle applies to hidden tasks', () => {
		const visible = filterProjects(PROJECTS, 'build');
		const project = PROJECTS.find((p) => p.project_id === visible[0].project_id)!;
		const next = toggleProject(project, all());
		expect([...next].sort()).toEqual([21]);
		expect(selectionTotals(PROJECTS, next)).toEqual({ minutes: 45, amount: 4500, gst_amount: 0 });
		expect([...toggleProject(project, next)].sort()).toEqual([11, 12, 21]);
	});

	it('mixed state', () => {
		expect(projectState(PROJECTS[0], all())).toBe('all');
		expect(projectState(PROJECTS[0], new Set([12]))).toBe('some');
		expect(projectState(PROJECTS[0], new Set([21]))).toBe('none');
		expect([...toggleProject(PROJECTS[0], new Set([12]))].sort()).toEqual([11, 12]);
	});

	it('totals count every selected task, hidden or not', () => {
		expect(selectionTotals(PROJECTS, all())).toEqual({ minutes: 135, amount: 22500, gst_amount: 1800 });
		expect(selectionTotals(PROJECTS, new Set())).toEqual({ minutes: 0, amount: 0, gst_amount: 0 });
	});

	it('payload echoes preview minutes and amount for selected tasks only', () => {
		expect(selectionPayload(PROJECTS, new Set([11, 21]))).toEqual([
			{ task_id: 11, minutes: 60, amount: 12000 },
			{ task_id: 21, minutes: 45, amount: 4500 }
		]);
	});
});

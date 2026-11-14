import { describe, expect, it } from 'vitest';
import { loadTable } from './table';

describe('loadTable', () => {
	const keys = ['name', 'in', 'want'] as const;

	it('returns the cases in order', () => {
		const cases = loadTable<{ name: string; in: string; want: number | null }>(
			'- {name: a, in: "1", want: 1}\n- {name: b, in: x, want: null}\n', 't.yaml', keys);
		expect(cases).toEqual([{ name: 'a', in: '1', want: 1 }, { name: 'b', in: 'x', want: null }]);
	});

	it('rejects an unknown key naming file, case and key', () => {
		expect(() => loadTable('- {name: a, wnat: 1}\n', 't.yaml', keys)).toThrow('t.yaml: case "a": unknown key "wnat"');
	});

	it('rejects a duplicate or missing name', () => {
		expect(() => loadTable('- {name: a}\n- {name: a}\n', 't.yaml', keys)).toThrow('t.yaml: duplicate case name "a"');
		expect(() => loadTable('- {in: x}\n', 't.yaml', keys)).toThrow('t.yaml: case 1 has no name');
	});

	it('rejects a document that is not a list', () => {
		expect(() => loadTable('name: a\n', 't.yaml', keys)).toThrow('t.yaml: want a list of cases');
	});
});

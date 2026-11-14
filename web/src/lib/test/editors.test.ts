// Runs every editor in editors/*.yaml through the editor contract
// (contract.ts): the AGENTS.md rules every editor must keep, checked the
// same way for each one.

import { describe, expect, it } from 'vitest';
import { CONTRACT_KEYS, contract, parseEditor } from './contract';

const files = import.meta.glob<string>('./editors/*.yaml', { query: '?raw', import: 'default', eager: true });

describe('editor YAML loader', () => {
	const valid = `
id: x
component: ProjectDialog
fill: {Project name: New}
submit: Create project
request: POST /api/v1/orgs/acme/projects
contract: {blocks_duplicate_submit: true}
skip: {${CONTRACT_KEYS.filter((k) => k !== 'blocks_duplicate_submit').map((k) => `${k}: n/a`).join(', ')}}
`;

	it('accepts a complete editor', () => {
		expect(parseEditor(valid, 'x.yaml').id).toBe('x');
	});

	it('rejects an unknown top-level key', () => {
		expect(() => parseEditor(valid + 'submti: Save\n', 'x.yaml')).toThrow('x.yaml: unknown key "submti"');
	});

	it('rejects an unknown contract key', () => {
		expect(() => parseEditor(valid.replace('blocks_duplicate_submit: true', 'blocks_duplicat_submit: true'), 'x.yaml'))
			.toThrow('x.yaml: unknown contract key "blocks_duplicat_submit"');
	});

	it('rejects a contract key that is neither checked nor skipped', () => {
		expect(() => parseEditor(valid.replace('cancel: n/a, ', ''), 'x.yaml')).toThrow(
			'x.yaml: contract key "cancel" is neither checked nor skipped with a reason'
		);
	});

	it('rejects an unknown component', () => {
		expect(() => parseEditor(valid.replace('ProjectDialog', 'NopeDialog'), 'x.yaml')).toThrow(
			'x.yaml: unknown component "NopeDialog"'
		);
	});
});

const editors = Object.entries(files).map(([path, source]) => parseEditor(source, path.replace('./', 'lib/test/')));

it('loads at least one editor', () => {
	expect(editors.length).toBeGreaterThan(0);
});

for (const editor of editors) {
	describe(`${editor.file} (${editor.id})`, () => {
		for (const key of CONTRACT_KEYS) {
			if (!(key in editor.contract)) continue;
			it(key, () => contract[key](editor));
		}
	});
}

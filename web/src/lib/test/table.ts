// loadTable parses a YAML list of test cases (imported with `?raw`). It
// rejects unknown keys and missing or duplicate names, so a typo can't
// silently skip a check and every case is addressable by name.

import { parse } from 'yaml';

export function loadTable<T>(source: string, file: string, keys: readonly string[]): T[] {
	const doc: unknown = parse(source);
	if (!Array.isArray(doc)) throw new Error(`${file}: want a list of cases`);
	const seen = new Set<string>();
	doc.forEach((c: Record<string, unknown>, i) => {
		const name = c?.name;
		if (typeof name !== 'string' || name === '') throw new Error(`${file}: case ${i + 1} has no name`);
		if (seen.has(name)) throw new Error(`${file}: duplicate case name "${name}"`);
		seen.add(name);
		for (const key of Object.keys(c)) {
			if (!keys.includes(key)) throw new Error(`${file}: case "${name}": unknown key "${key}"`);
		}
	});
	return doc as T[];
}

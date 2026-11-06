import { describe, expect, it, vi } from 'vitest';
import { flushSync, createRawSnippet } from 'svelte';
import { render } from '@testing-library/svelte';

// A reactive stand-in for $app/state's page (runes work here because this
// is a .svelte.test.ts file), so the test can switch org the way the org
// switcher's goto does: same layout instance, new page.params.org. The
// getter defers reading `page` until render time, after the hoisted mock
// factory has run and this declaration has been initialised.
const page = $state({ params: { org: 'acme' }, url: new URL('http://localhost/acme') });
vi.mock('$app/state', () => ({
	get page() {
		return page;
	}
}));
vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

import Layout from './+layout.svelte';

describe('[org] layout', () => {
	// Pages load their data once on mount, so switching org must remount
	// them - otherwise the previous org's figures stay on screen.
	it('remounts the page when the org changes', () => {
		let mounts = 0;
		const children = createRawSnippet(() => ({
			render: () => `<p data-testid="child">mount ${++mounts}</p>`
		}));

		const { getByTestId } = render(Layout, { children });
		expect(getByTestId('child').textContent).toBe('mount 1');

		page.params.org = 'globex';
		page.url = new URL('http://localhost/globex');
		flushSync();

		expect(getByTestId('child').textContent).toBe('mount 2');
	});
});

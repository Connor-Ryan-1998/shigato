import { beforeEach, describe, expect, it, vi } from 'vitest';
import { page, setPage } from '../../lib/test/app.svelte';
import { flushSync, createRawSnippet } from 'svelte';
import { render } from '@testing-library/svelte';

import Layout from './+layout.svelte';

beforeEach(() => setPage({ params: { org: 'acme' }, url: new URL('http://localhost/acme') }));

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

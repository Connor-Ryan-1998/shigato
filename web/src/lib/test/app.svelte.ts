// app.svelte backs the global $app/state and $app/navigation mocks
// installed by setup.ts. page is reactive, so components re-run their
// effects when a test changes the route params.

import { vi } from 'vitest';

type Page = { params: Record<string, string>; url: URL };

export const page: Page = $state({ params: {}, url: new URL('http://localhost/') });

export const goto = vi.fn();
export const beforeNavigate = vi.fn();
export const afterNavigate = vi.fn();
export const invalidate = vi.fn();
export const invalidateAll = vi.fn();
export const replaceState = vi.fn();
export const pushState = vi.fn();

export function setPage(next: { params?: Record<string, string>; url?: string | URL }): void {
	if (next.params) page.params = next.params;
	if (next.url) page.url = new URL(next.url, 'http://localhost/');
}

export function resetApp(): void {
	page.params = {};
	page.url = new URL('http://localhost/');
	for (const spy of [goto, beforeNavigate, afterNavigate, invalidate, invalidateAll, replaceState, pushState]) spy.mockReset();
}

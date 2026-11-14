// api-spy holds the function the mocked lib/api delegates to. It imports
// nothing from lib/api, so the mock factory in setup.ts can load it without
// a cycle; mock-api.ts re-exports it with the route helpers.

import { vi } from 'vitest';

// Untyped like a bare vi.fn(), so tests can type their implementation's body.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export const apiMock = vi.fn<(...args: any[]) => any>();

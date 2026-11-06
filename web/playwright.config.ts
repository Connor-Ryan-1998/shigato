import { defineConfig, devices } from '@playwright/test';

// The smoke test (e2e/smoke.spec.ts) starts its own Postgres database and
// shigoto binary in beforeAll, so there's no webServer block here. Run it
// with `task web:e2e`, which builds bin/shigoto (and the embedded SPA) first.
export default defineConfig({
	testDir: 'e2e',
	timeout: 60_000,
	retries: 0,
	workers: 1,
	reporter: process.env.CI ? 'github' : 'list',
	projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }]
});

import { defineConfig, devices } from '@playwright/test';

// The admin UI is served by the running ByteBucket container on its admin port.
// `make e2e-web` brings the container up before invoking Playwright; for local
// runs against an already-running instance, just `npx playwright test`.
const baseURL = process.env.BB_ADMIN_URL ?? 'http://localhost:9001';

export default defineConfig({
  testDir: './tests',
  // The whole suite drives one shared backend (a single container with one
  // user store), so tests run serially to avoid cross-test interference.
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  timeout: 30_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});

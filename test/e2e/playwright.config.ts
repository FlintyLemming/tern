import { defineConfig } from '@playwright/test'

// End-to-end tests against the real image: global-setup.ts starts the
// acceptance stack (test/acceptance/host/compose.yaml) and tears it down.
// The tests touch nothing but the browser and the host directories bound
// into the containers. Run them with `make e2e`.
export default defineConfig({
  testDir: '.',
  testMatch: '*.spec.ts',
  globalSetup: './global-setup.ts',
  workers: 1,
  fullyParallel: false,
  timeout: 15 * 60 * 1000,
  expect: { timeout: 30 * 1000 },
  reporter: [['list']],
  use: {
    // TERN_E2E_CHANNEL=chrome uses the installed Google Chrome instead of
    // Playwright's own Chromium (for machines that cannot download it).
    ...(process.env.TERN_E2E_CHANNEL ? { channel: process.env.TERN_E2E_CHANNEL } : {}),
    locale: 'zh-CN', // the interface follows the browser; the steps read the Chinese labels
    acceptDownloads: true,
    trace: 'retain-on-failure',
  },
})

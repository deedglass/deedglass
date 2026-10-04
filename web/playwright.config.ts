// Playwright for the Console (S24 §6). Runs through scripts/web_e2e.sh, which starts a throwaway
// PostgreSQL 18 and a real deedglass serve, and exports WEB_E2E_BASE. The browser is the installed
// Google Chrome (channel "chrome"), so nothing is downloaded.
import { defineConfig } from '@playwright/test';

const base = process.env.WEB_E2E_BASE ?? 'http://127.0.0.1:18241';

export default defineConfig({
  testDir: 'e2e',
  outputDir: 'test-results',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 30_000,
  reporter: [['list']],
  use: { baseURL: base, channel: 'chrome', trace: 'retain-on-failure', colorScheme: 'dark' },
  projects: [
    { name: 'setup', testMatch: /auth\.setup\.ts/ },
    { name: 'chromium', testMatch: /(shell|a11y|csp|live|kit|screens|perf)\.spec\.ts/, dependencies: ['setup'] },
    { name: 'shots', testMatch: /(screenshots|shots-s25)\.spec\.ts/, dependencies: ['setup'] },
  ],
});

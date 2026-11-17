import { readFileSync } from 'node:fs';
import { defineConfig } from '@playwright/test';

type Engine = 'chromium' | 'firefox' | 'webkit';

const engines: Engine[] = JSON.parse(readFileSync(new URL('./e2e/engines.json', import.meta.url), 'utf8'));

export default defineConfig({
  testDir: 'e2e',
  globalSetup: './e2e/global-setup.ts',
  outputDir: 'test-results',
  forbidOnly: true,
  globalTimeout: process.env.CI ? 25 * 60_000 : 0,
  fullyParallel: true,
  retries: 0,
  reporter: [['list']],
  use: {
    baseURL: process.env.ATRIUM_E2E_URL,
    trace: 'off',
  },
  projects: engines.map((engine) => ({ name: engine, use: { browserName: engine } })),
});

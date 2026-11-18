import { readFileSync } from 'node:fs';
import { defineConfig } from '@playwright/test';

type Engine = 'chromium' | 'firefox' | 'webkit';

const engines: Engine[] = JSON.parse(readFileSync(new URL('./e2e/engines.json', import.meta.url), 'utf8'));

export default defineConfig({
  testDir: 'e2e/live',
  outputDir: 'test-results',
  forbidOnly: true,
  retries: 0,
  workers: 1,
  reporter: [['list']],
  use: { trace: 'off' },
  projects: engines.map((engine) => ({ name: engine, use: { browserName: engine } })),
});

import { test as base, expect, type Page } from '@playwright/test';
import type { MockScenario } from '../src/mocks/handlers.ts';

export { expect };

export interface Layout {
  name: string;
  width: number;
  height: number;
  touch: boolean;
}

export const layouts: readonly Layout[] = [
  { name: 'phone', width: 390, height: 844, touch: true },
  { name: 'tablet', width: 820, height: 1180, touch: true },
  { name: 'desktop', width: 1440, height: 900, touch: false },
];

export const galleryLayouts: readonly Layout[] = [
  { name: 'small phone', width: 360, height: 740, touch: true },
  ...layouts,
  { name: 'large phone', width: 430, height: 932, touch: true },
];

export const routes = [
  { path: '/', title: 'Home' },
  { path: '/inbox', title: 'Inbox' },
  { path: '/apps', title: 'Apps' },
  { path: '/spaces', title: 'Spaces' },
  { path: '/settings', title: 'Settings' },
] as const;

export async function useScenario(page: Page, scenario: MockScenario): Promise<void> {
  await page.addInitScript((value) => localStorage.setItem('atrium.mock', value), JSON.stringify(scenario));
}

export const test = base.extend<{ problems: string[]; allowStatus: number[] }>({
  allowStatus: [[], { option: true }],
  problems: [
    async ({ page, allowStatus }, use) => {
      const problems: string[] = [];
      page.on('console', (message) => {
        if (message.type() !== 'error') return;
        const status = /status of (\d+)/.exec(message.text())?.[1];
        if (status && allowStatus.includes(Number(status))) return;
        problems.push(`console error: ${message.text()}`);
      });
      page.on('pageerror', (error) => problems.push(`uncaught: ${error.message}`));
      await page.addInitScript(() => {
        document.addEventListener('securitypolicyviolation', (event) => {
          console.error(`policy violation: ${event.violatedDirective} blocked ${event.blockedURI || 'inline'}`);
        });
      });
      await use(problems);
      expect(problems, 'console errors, uncaught errors and policy violations').toEqual([]);
    },
    { auto: true },
  ],
});

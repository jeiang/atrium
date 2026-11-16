import { test as base, expect } from '@playwright/test';

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
  { path: '/', title: 'Today' },
  { path: '/apps', title: 'Apps' },
] as const;

export const test = base.extend<{ problems: string[] }>({
  problems: [
    async ({ page }, use) => {
      const problems: string[] = [];
      page.on('console', (message) => {
        if (message.type() === 'error') problems.push(`console error: ${message.text()}`);
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

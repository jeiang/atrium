import { test as base, type ConsoleMessage, expect, type Page } from '@playwright/test';
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
  { path: '/apps/directory', title: 'All apps' },
  { path: '/settings', title: 'Settings' },
] as const;

export async function useScenario(page: Page, scenario: MockScenario): Promise<void> {
  await page.addInitScript((value) => localStorage.setItem('atrium.mock', value), JSON.stringify(scenario));
}

export const unreachableConsole = [
  /Failed to load resource|net::ERR_|NetworkError|Load failed|network connection|served by service worker is an error|passed an Error Response to FetchEvent|ServiceWorker intercepted the request/,
];

export async function setUnreachable(page: Page, down: boolean): Promise<void> {
  await page.evaluate((value) => {
    const state = JSON.parse(localStorage.getItem('atrium.mock') ?? '{}');
    localStorage.setItem('atrium.mock', JSON.stringify({ ...state, unreachable: value }));
  }, down);
}

function unreachableNotice(page: Page) {
  return page.getByRole('status').filter({ hasText: 'Cannot reach atrium.' });
}

export async function goUnreachable(page: Page): Promise<void> {
  await setUnreachable(page, true);
  await page.evaluate(() => dispatchEvent(new Event('offline')));
  await expect(unreachableNotice(page)).toBeVisible();
}

export async function goReachable(page: Page): Promise<void> {
  await setUnreachable(page, false);
  await expect(unreachableNotice(page)).toHaveCount(0, { timeout: 15_000 });
}

export async function storedKeys(page: Page): Promise<string[]> {
  return page.evaluate(async () => {
    const opened = Promise.withResolvers<IDBDatabase | undefined>();
    const request = indexedDB.open('atrium-read-cache');
    request.onupgradeneeded = () => request.transaction?.abort();
    request.onsuccess = () => opened.resolve(request.result);
    request.onerror = () => opened.resolve(undefined);
    const db = await opened.promise;
    if (!db) return [];
    const keys = Promise.withResolvers<string[]>();
    const read = db.transaction('entries', 'readonly').objectStore('entries').getAllKeys();
    read.onsuccess = () => keys.resolve(read.result.map(String));
    read.onerror = () => keys.resolve([]);
    const result = await keys.promise;
    db.close();
    return result;
  });
}

const mockStreamAborted =
  /ServiceWorker intercepted the request and encountered an unexpected error.*mockServiceWorker\.js/;
const firefoxUnlocatedInvalidState =
  '[JavaScript Error: "InvalidStateError: An attempt was made to use an object that is not, or is no longer, usable"]';
const resizeObserverDeferred = 'ResizeObserver loop completed with undelivered notifications';
const firefoxFontAborted = /downloadable font: download failed .*status=2152398850 /;

function unlocatedFirefoxInvalidState(browserName: string, message: ConsoleMessage): boolean {
  const { url, lineNumber, columnNumber } = message.location();
  return (
    browserName === 'firefox' &&
    message.text() === firefoxUnlocatedInvalidState &&
    url === '' &&
    lineNumber === 0 &&
    columnNumber === 0
  );
}

export const test = base.extend<{ problems: string[]; allowStatus: number[]; allowConsole: RegExp[] }>({
  allowStatus: [[], { option: true }],
  allowConsole: [[], { option: true }],
  problems: [
    async ({ page, allowStatus, allowConsole, browserName }, use) => {
      const problems: string[] = [];
      page.on('console', (message) => {
        if (message.type() !== 'error') return;
        const status = /status of (\d+)/.exec(message.text())?.[1];
        if (status && allowStatus.includes(Number(status))) return;
        if (mockStreamAborted.test(message.text())) return;
        if (unlocatedFirefoxInvalidState(browserName, message)) return;
        if (allowConsole.some((pattern) => pattern.test(message.text()))) return;
        if (browserName === 'webkit' && message.text().includes(resizeObserverDeferred)) return;
        if (browserName === 'firefox' && firefoxFontAborted.test(message.text())) return;
        problems.push(`console error: ${message.text()}`);
      });
      page.on('pageerror', (error) => {
        if (allowConsole.some((pattern) => pattern.test(error.message))) return;
        if (browserName === 'webkit' && error.message.startsWith(resizeObserverDeferred)) return;
        problems.push(`uncaught: ${error.message}`);
      });
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

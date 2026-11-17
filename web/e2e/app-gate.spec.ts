import type { Page } from '@playwright/test';
import { gotoShell } from './checks.ts';
import { expect, test } from './fixtures.ts';

const T0 = Date.parse('2026-11-16T12:00:00Z');
const pages = [
  { app: 'calendar', path: '/apps/calendar' },
  { app: 'media', path: '/apps/media' },
  { app: 'media', path: '/apps/media/item/show/s1' },
  { app: 'torrents', path: '/apps/torrents' },
];

function scenario(page: Page, value: object): Promise<void> {
  return page.evaluate((text) => localStorage.setItem('atrium.mock', text), JSON.stringify(value));
}

function cachedAppsAt(page: Page): Promise<number | undefined> {
  return page.evaluate(async () => {
    const opened = Promise.withResolvers<IDBDatabase | undefined>();
    const request = indexedDB.open('atrium-read-cache');
    request.onupgradeneeded = () => request.transaction?.abort();
    request.onsuccess = () => opened.resolve(request.result);
    request.onerror = () => opened.resolve(undefined);
    const db = await opened.promise;
    if (!db) return undefined;
    try {
      const read = db.transaction('entries', 'readonly').objectStore('entries').get('["apps"]');
      const done = Promise.withResolvers<{ updatedAt: number } | undefined>();
      read.onsuccess = () => done.resolve(read.result);
      read.onerror = () => done.resolve(undefined);
      return (await done.promise)?.updatedAt;
    } finally {
      db.close();
    }
  });
}

test.describe('an out-of-date cached app list', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  for (const { app, path } of pages) {
    test(`${path} does not show Not found while the fresh list loads`, async ({ page }) => {
      await page.clock.setFixedTime(T0);
      await gotoShell(page, '/');
      await expect.poll(() => cachedAppsAt(page)).toBe(T0);
      await scenario(page, { apps: { off: [app] } });
      await page.clock.setFixedTime(T0 + 60_000);
      await gotoShell(page, path);
      await expect(page.getByRole('heading', { level: 1, name: 'Not found' })).toBeVisible();
      await expect.poll(() => cachedAppsAt(page)).toBe(T0 + 60_000);
      await page.waitForTimeout(500);

      await scenario(page, { apps: { readDelay: 1_500 } });
      await page.clock.setFixedTime(T0 + 120_000);
      await page.addInitScript(() => {
        const seen: string[] = [];
        Reflect.set(globalThis, 'atriumHeadings', seen);
        new MutationObserver(() => {
          const text = document.querySelector('main h1')?.textContent?.trim();
          if (text && text !== seen.at(-1)) seen.push(text);
        }).observe(document, { subtree: true, childList: true, characterData: true });
      });
      await gotoShell(page, path);
      await expect.poll(() => page.evaluate(() => Reflect.get(globalThis, 'atriumAppsReads') ?? 0)).toBeGreaterThan(0);
      await page.waitForTimeout(2_500);
      await expect(page.getByRole('heading', { level: 1 })).not.toHaveText('Not found');
      expect(await page.evaluate(() => Reflect.get(globalThis, 'atriumHeadings') ?? [])).not.toContain('Not found');
    });
  }
});

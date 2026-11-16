import { execFileSync } from 'node:child_process';
import { cp, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import type { ServerResponse } from 'node:http';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import type { Page } from '@playwright/test';
import { expectNoHorizontalScroll, expectNoSeriousAxeFindings, expectTouchTargets } from './checks.ts';
import { test as base, expect, layouts } from './fixtures.ts';
import { type Handler, type ServeState, serve } from './server.ts';

const dist = resolve(process.env.ATRIUM_WEB_DIST ?? 'dist');
const CSRF = 'csrf-fictional-7f3a91';
const T0 = new Date('2026-03-02T09:00:00Z').getTime();
const HOUR = 3_600_000;
const DAY = 24 * HOUR;

interface Api {
  subject: string;
  signedIn: boolean;
  needsYou: number;
  logouts: number;
  calls: string[];
}

interface Site {
  url: string;
  state: ServeState;
  api: Api;
  down(on: boolean): void;
}

interface Stored {
  entries: { key: string; data: unknown }[];
  meta: Record<string, unknown>;
}

const names: Record<string, string> = { 'op-1': 'Ada Operator', 'op-2': 'Grace Operator' };

function shell(needsYou: number): unknown {
  return {
    brand_url: '/',
    footer: [],
    groups: [],
    sections: [],
    settings: [],
    settings_url: '/settings',
    needs_you: needsYou,
    notify_failing: [],
    mail: { mode: 'live', paused: false },
    modules: ['mail', 'calendar', 'notion', 'media', 'assistant', 'notifications'].map((name) => ({
      name,
      state: 'enabled',
    })),
    version: '0.0.0-offline',
  };
}

function json(res: ServerResponse, status: number, body?: unknown): true {
  if (body === undefined) {
    res.writeHead(status).end();
  } else {
    res
      .writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' })
      .end(JSON.stringify(body));
  }
  return true;
}

function fakeApi(api: Api): Handler {
  return (req, res, pathname) => {
    const method = req.method ?? 'GET';
    if (pathname === '/login' || pathname.startsWith('/auth/')) {
      const title = pathname === '/login' ? 'Sign in' : 'Callback';
      res
        .writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' })
        .end(`<!doctype html><title>${title}</title><h1>${title}</h1>`);
      return true;
    }
    if (!pathname.startsWith('/web/v1/')) return false;
    const path = pathname.slice('/web/v1'.length);
    api.calls.push(`${method} ${path}`);
    if (path === '/session' && method === 'GET') {
      if (!api.signedIn) return json(res, 401, { error: { code: 'unauthenticated', message: 'Sign in to continue.' } });
      return json(res, 200, {
        auth_mode: 'oidc',
        csrf_token: CSRF,
        identity: { subject: api.subject, name: names[api.subject], email: `${api.subject}@example.test` },
        sign_out: { method: 'POST', url: '/web/v1/session/logout' },
      });
    }
    if (path === '/session/logout' && method === 'POST') {
      api.logouts += 1;
      api.signedIn = false;
      return json(res, 204);
    }
    if (path === '/shell') return json(res, 200, shell(api.needsYou));
    if (path === '/events') {
      res.writeHead(200, { 'Content-Type': 'text/event-stream' }).end('event: sync\ndata: {"topics":[]}\n\n');
      return true;
    }
    return json(res, 404, { error: { code: 'not_found', message: 'Not found.' } });
  };
}

const test = base.extend<{ site: Site }>({
  site: async ({ browserName: _ }, use) => {
    const state: ServeState = {};
    const api: Api = { subject: 'op-1', signedIn: true, needsYou: 3, logouts: 0, calls: [] };
    const { url, server } = await serve(dist, { base: '/next/', state, handle: fakeApi(api) });
    await use({
      url,
      state,
      api,
      down(on) {
        state.down = on;
      },
    });
    server.closeAllConnections();
    const { promise, resolve: closed } = Promise.withResolvers<void>();
    server.close(() => closed());
    await promise;
  },
});

test.use({
  allowConsole: /Failed to load resource|net::ERR_|NetworkError|Load failed|network connection/,
});

async function signedIn(page: Page, url: string, name = 'Ada Operator'): Promise<void> {
  await page.goto(url);
  await expect(page.getByRole('button', { name: `Account: ${name}` })).toBeVisible();
}

async function workerReady(page: Page): Promise<void> {
  await page.evaluate(async () => {
    await navigator.serviceWorker.ready;
  });
  await expect.poll(() => shellCaches(page)).toHaveLength(1);
}

function shellCaches(page: Page): Promise<string[]> {
  return page.evaluate(async () => (await caches.keys()).filter((key) => key.startsWith('next-shell-')));
}

function readStored(page: Page): Promise<Stored> {
  return page.evaluate(async () => {
    const opened = Promise.withResolvers<IDBDatabase | undefined>();
    const request = indexedDB.open('atrium-read-cache');
    request.onupgradeneeded = () => request.transaction?.abort();
    request.onsuccess = () => opened.resolve(request.result);
    request.onerror = () => opened.resolve(undefined);
    const db = await opened.promise;
    if (!db) return { entries: [], meta: {} };
    try {
      const tx = db.transaction(['entries', 'meta'], 'readonly');
      const entries = tx.objectStore('entries').getAll();
      const metaStore = tx.objectStore('meta');
      const keys = metaStore.getAllKeys();
      const values = metaStore.getAll();
      const done = Promise.withResolvers<void>();
      tx.oncomplete = () => done.resolve();
      tx.onerror = () => done.reject(tx.error);
      await done.promise;
      const meta: Record<string, unknown> = {};
      keys.result.forEach((key, index) => {
        meta[String(key)] = values.result[index];
      });
      return { entries: entries.result as { key: string; data: unknown }[], meta };
    } finally {
      db.close();
    }
  });
}

async function storedShell(page: Page): Promise<number | undefined> {
  const { entries } = await readStored(page);
  const entry = entries.find((item) => item.key === '["shell"]');
  return (entry?.data as { needs_you?: number } | undefined)?.needs_you;
}

function inboxCount(page: Page) {
  return page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: /^Inbox/ });
}

async function openAccount(page: Page, name = 'Ada Operator'): Promise<void> {
  await page.getByRole('button', { name: `Account: ${name}` }).click();
  await expect(page.getByRole('menu', { name: 'Account' })).toBeVisible();
}

async function buildUpdate(): Promise<{ root: string; entry: string }> {
  const root = await mkdtemp(join(tmpdir(), 'atrium-next-update-'));
  await cp(dist, root, { recursive: true });
  execFileSync('chmod', ['-R', 'u+w', root]);
  const html = await readFile(join(root, 'index.html'), 'utf8');
  const old = /\/next\/assets\/(index-[\w-]+)\.js/.exec(html)?.[1];
  if (!old) throw new Error('no entry script in index.html');
  const entry = `${old}u`;
  await cp(join(root, 'assets', `${old}.js`), join(root, 'assets', `${entry}.js`));
  await rm(join(root, 'assets', `${old}.js`));
  await writeFile(join(root, 'index.html'), html.replaceAll(`${old}.js`, `${entry}.js`));
  const worker = await readFile(join(root, 'sw.js'), 'utf8');
  await writeFile(
    join(root, 'sw.js'),
    worker.replaceAll(`${old}.js`, `${entry}.js`).replace(/const VERSION = "[^"]+";/, 'const VERSION = "update";'),
  );
  return { root, entry };
}

test.describe('the cached shell', () => {
  test('a second visit opens the frame and deep routes with the server down', async ({ page, site }) => {
    await signedIn(page, `${site.url}/next/`);
    await workerReady(page);
    await expect.poll(() => storedShell(page)).toBe(3);
    site.down(true);
    await page.reload();
    await expect(page.getByRole('button', { name: 'Account: Ada Operator' })).toBeVisible();
    await expect(page.getByRole('status').filter({ hasText: 'Cannot reach atrium.' })).toBeVisible();
    await expect(inboxCount(page)).toContainText('3');
    await page.goto(`${site.url}/next/apps`);
    await expect(page.getByRole('heading', { level: 1, name: 'Apps' })).toBeVisible();
    await page.goto(`${site.url}/next/spaces`);
    await expect(page.getByRole('heading', { level: 1, name: 'Spaces' })).toBeVisible();
  });

  test('a page missing from the device shows that it is not available', async ({ page, site }) => {
    await signedIn(page, `${site.url}/next/`);
    await workerReady(page);
    const removed = await page.evaluate(async () => {
      const [name] = (await caches.keys()).filter((key) => key.startsWith('next-shell-'));
      const cache = await caches.open(name ?? '');
      const gone: string[] = [];
      for (const request of await cache.keys()) {
        if (!/\/assets\/AppsPage-/.test(request.url)) continue;
        gone.push(request.url);
        await cache.delete(request);
      }
      return gone;
    });
    expect(removed).toHaveLength(1);
    site.down(true);
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Apps', exact: true }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Not available' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Try again' }).first()).toBeVisible();
  });

  test('a browser that reports offline while atrium answers stays online', async ({ page, site }) => {
    await page.addInitScript(() => {
      Object.defineProperty(Navigator.prototype, 'onLine', { get: () => false });
    });
    await signedIn(page, `${site.url}/next/`);
    await expect(inboxCount(page)).toContainText('3');
    const probes = site.api.calls.length;
    await page.evaluate(() => dispatchEvent(new Event('offline')));
    await expect.poll(() => site.api.calls.length).toBeGreaterThan(probes);
    await expect(page.locator('.offline-status')).toHaveCount(0);
    await openAccount(page);
    await page.getByRole('menuitem', { name: 'Sign out' }).click();
    await expect.poll(() => site.api.logouts).toBe(1);
  });

  test('the worker never answers API, stream or sign-in requests', async ({ page, site }) => {
    const fromWorker: string[] = [];
    page.on('response', (response) => {
      const path = new URL(response.url()).pathname;
      if (!path.startsWith('/next/') && response.fromServiceWorker()) fromWorker.push(path);
    });
    await signedIn(page, `${site.url}/next/`);
    await workerReady(page);
    await page.reload();
    await expect(page.getByRole('button', { name: 'Account: Ada Operator' })).toBeVisible();
    expect(await page.evaluate(() => navigator.serviceWorker.controller !== null)).toBe(true);
    await expect.poll(() => site.api.calls.filter((call) => call === 'GET /session').length).toBeGreaterThan(1);
    await expect.poll(() => site.api.calls.includes('GET /events')).toBe(true);
    await page.goto(`${site.url}/login?next=/next/`);
    await expect(page.getByRole('heading', { level: 1, name: 'Sign in' })).toBeVisible();
    await page.goto(`${site.url}/auth/callback?state=fictional`);
    await expect(page.getByRole('heading', { level: 1, name: 'Callback' })).toBeVisible();
    expect(fromWorker).toEqual([]);
    site.down(true);
    const outside = await page.goto(`${site.url}/settings`).catch(() => undefined);
    expect(outside?.ok() ?? false).toBe(false);
  });

  test('an update waits for the next navigation and removes the old cache', async ({ page, site }) => {
    await signedIn(page, `${site.url}/next/`);
    await workerReady(page);
    await page.reload();
    await expect(page.getByRole('button', { name: 'Account: Ada Operator' })).toBeVisible();
    const [first] = await shellCaches(page);
    const update = await buildUpdate();
    try {
      site.state.root = update.root;
      await page.evaluate(() => {
        Reflect.set(window, 'atriumMarker', 'same page');
      });
      await page.evaluate(async () => {
        const registration = await navigator.serviceWorker.getRegistration();
        await registration?.update();
      });
      await expect
        .poll(() =>
          page.evaluate(async () => (await navigator.serviceWorker.getRegistration())?.waiting?.state ?? 'none'),
        )
        .toBe('installed');
      expect(await shellCaches(page)).toEqual(expect.arrayContaining([first, 'next-shell-update']));
      expect(await page.evaluate(() => Reflect.get(window, 'atriumMarker'))).toBe('same page');
      await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Apps', exact: true }).click();
      await expect(page).toHaveURL(/\/next\/apps$/);
      await expect(page.getByRole('heading', { level: 1, name: 'Apps' })).toBeVisible();
      await expect.poll(() => page.evaluate(() => Reflect.get(window, 'atriumMarker') ?? 'reloaded')).toBe('reloaded');
      expect(await page.locator(`script[src*="${update.entry}"]`).count()).toBe(1);
      await expect.poll(() => shellCaches(page)).toEqual(['next-shell-update']);
    } finally {
      await rm(update.root, { recursive: true, force: true });
    }
  });
});

test.describe('the read cache', () => {
  test('cached data shows its age offline and refreshes on reconnect', async ({ page, site, context }) => {
    await page.clock.setFixedTime(T0);
    await signedIn(page, `${site.url}/next/`);
    await workerReady(page);
    await expect.poll(() => storedShell(page)).toBe(3);
    site.down(true);
    await page.clock.setFixedTime(T0 + 3 * HOUR);
    await page.reload();
    const status = page.getByRole('status').filter({ hasText: 'atrium' });
    await expect(status).toContainText('Cannot reach atrium. Showing data from 3 hours ago.');
    await expect(inboxCount(page)).toContainText('3');
    await context.setOffline(true);
    await expect(status).toContainText(
      'Cannot reach atrium. Showing data from 3 hours ago. Nothing can be sent until it answers.',
    );
    site.api.needsYou = 5;
    site.down(false);
    await context.setOffline(false);
    await page.evaluate(() => dispatchEvent(new Event('online')));
    await expect(page.getByRole('status').filter({ hasText: 'Cannot reach atrium.' })).toHaveCount(0);
    await expect(inboxCount(page)).toContainText('5');
    await expect.poll(() => site.api.calls.includes('GET /events')).toBe(true);
  });

  test('try again reloads the data once the server is back', async ({ page, site }) => {
    await signedIn(page, `${site.url}/next/`);
    await workerReady(page);
    await expect.poll(() => storedShell(page)).toBe(3);
    site.down(true);
    await page.reload();
    await expect(page.getByRole('status').filter({ hasText: 'Cannot reach atrium.' })).toBeVisible();
    site.api.needsYou = 4;
    site.down(false);
    await page.getByRole('button', { name: 'Try again' }).click();
    await expect(page.getByRole('status').filter({ hasText: 'Cannot reach atrium.' })).toHaveCount(0);
    await expect(inboxCount(page)).toContainText('4');
  });

  test('the page finds atrium again by itself without a browser event', async ({ page, site }) => {
    await page.clock.install();
    await signedIn(page, `${site.url}/next/`);
    await workerReady(page);
    await expect.poll(() => storedShell(page)).toBe(3);
    site.down(true);
    await page.reload();
    await expect(page.getByRole('status').filter({ hasText: 'Cannot reach atrium.' })).toBeVisible();
    site.api.needsYou = 6;
    site.down(false);
    await page.clock.runFor(70_000);
    await expect(page.getByRole('status').filter({ hasText: 'Cannot reach atrium.' })).toHaveCount(0);
    await expect(inboxCount(page)).toContainText('6');
  });

  test('data older than seven days is dropped', async ({ page, site }) => {
    await page.clock.setFixedTime(T0);
    await signedIn(page, `${site.url}/next/`);
    await workerReady(page);
    await expect.poll(() => storedShell(page)).toBe(3);
    await page.clock.setFixedTime(T0 + 6 * DAY);
    await page.reload();
    await expect(page.getByRole('button', { name: 'Account: Ada Operator' })).toBeVisible();
    await expect.poll(() => storedShell(page)).toBe(3);
    site.down(true);
    await page.clock.setFixedTime(T0 + 13 * DAY + HOUR);
    await page.reload();
    await expect(page.getByRole('heading', { level: 1, name: 'Cannot reach atrium' })).toBeVisible();
    expect(await storedShell(page)).toBeUndefined();
  });

  test('signing out, a 401 and a different identity clear the cache', async ({ page, site }) => {
    await signedIn(page, `${site.url}/next/`);
    await expect.poll(() => storedShell(page)).toBe(3);
    site.api.subject = 'op-2';
    site.api.needsYou = 7;
    await page.reload();
    await expect(page.getByRole('button', { name: 'Account: Grace Operator' })).toBeVisible();
    await expect.poll(() => storedShell(page)).toBe(7);
    expect((await readStored(page)).meta.session).toMatchObject({ identity: { subject: 'op-2' } });
    await openAccount(page, 'Grace Operator');
    await page.getByRole('menuitem', { name: 'Sign out' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'You are signed out' })).toBeVisible();
    expect(site.api.logouts).toBe(1);
    await expect.poll(async () => (await readStored(page)).entries).toEqual([]);
    expect((await readStored(page)).meta).toEqual({});
    site.api.signedIn = true;
    await signedIn(page, `${site.url}/next/`, 'Grace Operator');
    await expect.poll(() => storedShell(page)).toBe(7);
    site.api.signedIn = false;
    await page.reload();
    await expect.poll(async () => (await readStored(page)).meta).toEqual({});
    expect((await readStored(page)).entries).toEqual([]);
  });

  test('the device switch deletes data at once and stops keeping it', async ({ page, site }) => {
    await signedIn(page, `${site.url}/next/`);
    await workerReady(page);
    await expect.poll(() => storedShell(page)).toBe(3);
    await openAccount(page);
    const toggle = page.getByRole('menuitemcheckbox', { name: "Don't keep data on this device" });
    await expect(toggle).toHaveAttribute('aria-checked', 'false');
    await toggle.click();
    await expect(page.getByText('Data on this device deleted.').first()).toBeVisible();
    await expect(toggle).toHaveAttribute('aria-checked', 'true');
    expect(await page.evaluate(() => localStorage.getItem('atrium.device.forget'))).toBe('1');
    await expect.poll(async () => (await readStored(page)).entries).toEqual([]);
    await page.reload();
    await expect(page.getByRole('button', { name: 'Account: Ada Operator' })).toBeVisible();
    await expect(inboxCount(page)).toContainText('3');
    expect(await readStored(page)).toEqual({ entries: [], meta: {} });
    site.down(true);
    await page.reload();
    await expect(page.getByRole('heading', { level: 1, name: 'Cannot reach atrium' })).toBeVisible();
    site.down(false);
    await page.reload();
    await openAccount(page);
    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-checked', 'false');
    await expect.poll(() => storedShell(page)).toBe(3);
  });

  test('an offline sign-out is refused and never sent later', async ({ page, site, context }) => {
    await signedIn(page, `${site.url}/next/`);
    await context.setOffline(true);
    await expect(page.getByRole('status').filter({ hasText: 'Cannot reach atrium.' })).toBeVisible();
    await openAccount(page);
    await page.getByRole('menuitem', { name: 'Sign out' }).click();
    await expect(page.getByText('You are offline. Nothing was sent.').first()).toBeVisible();
    await context.setOffline(false);
    await expect(page.getByRole('status').filter({ hasText: 'Cannot reach atrium.' })).toHaveCount(0);
    await page.waitForTimeout(1_500);
    expect(site.api.logouts).toBe(0);
    expect(site.api.calls.filter((call) => call.startsWith('POST'))).toEqual([]);
    await expect(page.getByRole('button', { name: 'Account: Ada Operator' })).toBeVisible();
  });

  test('the device keeps no session token or credential', async ({ page, site }) => {
    await signedIn(page, `${site.url}/next/`);
    await workerReady(page);
    await expect.poll(() => storedShell(page)).toBe(3);
    const stored = await readStored(page);
    expect(Object.keys(stored.meta).sort()).toEqual(['format', 'seen', 'session']);
    expect(stored.meta.session).toEqual({
      auth_mode: 'oidc',
      identity: { subject: 'op-1', name: 'Ada Operator', email: 'op-1@example.test' },
      sign_out: { method: 'POST', url: '/web/v1/session/logout' },
    });
    expect(stored.entries.map((entry) => entry.key)).toEqual(['["shell"]']);
    const everything = await page.evaluate(async () => {
      const texts: string[] = [JSON.stringify({ ...localStorage }), JSON.stringify({ ...sessionStorage })];
      for (const key of await caches.keys()) {
        const cache = await caches.open(key);
        for (const request of await cache.keys()) {
          texts.push(request.url);
          if (!request.url.endsWith('.woff2')) texts.push((await (await cache.match(request))?.text()) ?? '');
        }
      }
      return texts.join('\n');
    });
    expect(JSON.stringify(stored)).not.toContain(CSRF);
    expect(everything).not.toContain(CSRF);
    expect(everything).not.toContain('op-1@example.test');
  });
});

test.describe('the offline status layout', () => {
  for (const layout of layouts) {
    test(`fits the ${layout.name} layout`, async ({ page, site }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await signedIn(page, `${site.url}/next/`);
      await workerReady(page);
      await expect.poll(() => storedShell(page)).toBe(3);
      site.down(true);
      await page.reload();
      await expect(page.getByRole('button', { name: 'Try again' })).toBeVisible();
      await page.evaluate(() => document.fonts.ready);
      await expectNoHorizontalScroll(page);
      await expectTouchTargets(page, '.offline-status');
      await expectNoSeriousAxeFindings(page);
    });
  }
});

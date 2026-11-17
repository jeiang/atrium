import { readFileSync } from 'node:fs';
import type { IncomingMessage, ServerResponse } from 'node:http';
import { resolve } from 'node:path';
import type { BrowserContext, Page, Worker } from '@playwright/test';
import type { Draft } from '../src/contract/index.ts';
import { type Api, fakeApi, signedInApi } from './api.ts';
import { test as base, expect } from './fixtures.ts';
import { serve } from './server.ts';

const dist = resolve(process.env.ATRIUM_WEB_DIST ?? 'dist');
const classicWorker = readFileSync(resolve('../internal/web/assets/sw.js'), 'utf8').replace('__ATRIUM_ASSETS__', '{}');

const draft: Draft = {
  id: 7,
  account: 'sam@example.test',
  account_configured: true,
  to: 'Ilse de Wit <ilse@example.test>',
  cc: '',
  subject: 'Shared garden plans',
  body: 'Shall we split the seed order this year?',
  status: 'draft',
  revision: 3,
  created_at: 1_795_000_000,
  updated_at: 1_795_003_600,
  unexpected_recipients: [],
};

interface Site {
  url: string;
  api: Api;
}

function classic(_req: IncomingMessage, res: ServerResponse, pathname: string): boolean {
  const pages: Record<string, [string, string]> = {
    '/old': ['text/html; charset=utf-8', '<!doctype html><title>Old page</title><script src="/old.js"></script>'],
    '/old.js': ['text/javascript; charset=utf-8', "navigator.serviceWorker.register('/sw.js', { scope: '/' });"],
    '/sw.js': ['text/javascript; charset=utf-8', classicWorker],
  };
  const page = pages[pathname];
  if (!page) return false;
  res.writeHead(200, { 'Content-Type': page[0], 'Cache-Control': 'no-cache' }).end(page[1]);
  return true;
}

const test = base.extend<{ site: Site }>({
  site: async ({ browserName: _ }, use) => {
    const api: Api = { ...signedInApi(), drafts: [draft] };
    const handle = fakeApi(api);
    const { url, server } = await serve(dist, {
      base: '/next/',
      handle: (req, res, pathname) => classic(req, res, pathname) || handle(req, res, pathname),
    });
    await use({ url, api });
    server.closeAllConnections();
    const { promise, resolve: closed } = Promise.withResolvers<void>();
    server.close(() => closed());
    await promise;
  },
});

test.use({ allowStatus: [401, 404] });

function card(page: Page) {
  return page.locator('[data-approval="draft:7"]');
}

function unsafe(api: Api): string[] {
  return api.calls.filter((call) => !call.startsWith('GET '));
}

async function controlled(page: Page, url: string): Promise<void> {
  await page.goto(url);
  await page.evaluate(async () => {
    await navigator.serviceWorker.ready;
  });
  await page.reload();
  await expect.poll(() => page.evaluate(() => Boolean(navigator.serviceWorker.controller))).toBe(true);
}

async function workerAt(context: BrowserContext, script: string): Promise<Worker> {
  await expect
    .poll(() => context.serviceWorkers().some((worker) => new URL(worker.url()).pathname === script))
    .toBe(true);
  const worker = context.serviceWorkers().find((item) => new URL(item.url()).pathname === script);
  if (!worker) throw new Error(`no worker at ${script}`);
  return worker;
}

async function click(worker: Worker, url: string): Promise<void> {
  await worker.evaluate((target) => {
    const scope = globalThis as unknown as { ExtendableEvent: typeof Event; dispatchEvent(event: Event): boolean };
    const event = new scope.ExtendableEvent('notificationclick');
    Object.defineProperty(event, 'notification', { value: { data: { url: target }, close() {} } });
    scope.dispatchEvent(event);
  }, url);
}

test.describe('a push notification click', () => {
  test.skip(({ browserName }) => browserName !== 'chromium', 'Playwright reaches service workers only in Chromium');

  test('opens the card in the open window and sends no write', async ({ page, context, site }) => {
    await controlled(page, `${site.url}/next/`);
    await click(await workerAt(context, '/next/sw.js'), '/next/drafts/7');
    await expect(page).toHaveURL(/\/next\/inbox\/draft(:|%3A)7$/);
    await expect(card(page)).toContainText('Shared garden plans');
    expect(unsafe(site.api)).toEqual([]);
  });

  test('a notification shown before the move still opens the card', async ({ page, context, site }) => {
    await controlled(page, `${site.url}/next/`);
    await click(await workerAt(context, '/next/sw.js'), '/drafts/7');
    await expect(page).toHaveURL(/\/next\/inbox\/draft(:|%3A)7$/);
    await expect(card(page)).toContainText('Shared garden plans');
    expect(unsafe(site.api)).toEqual([]);
  });

  test('a decided or unknown item lands on the Inbox with a plain message', async ({ page, context, site }) => {
    await controlled(page, `${site.url}/next/`);
    await click(await workerAt(context, '/next/sw.js'), '/next/proposals/99');
    await expect(page).toHaveURL(/\/next\/inbox\/proposal(:|%3A)99$/);
    await expect(page.getByRole('heading', { name: 'Nothing waits here' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Back to the Inbox' })).toBeVisible();
    expect(unsafe(site.api)).toEqual([]);
  });

  test('an existing subscription on the classic worker opens the card in the new client', async ({
    page,
    context,
    site,
  }) => {
    await controlled(page, `${site.url}/old`);
    await click(await workerAt(context, '/sw.js'), '/next/drafts/7');
    await expect(page).toHaveURL(/\/next\/inbox\/draft(:|%3A)7$/);
    await expect(card(page)).toContainText('Shared garden plans');
    expect(unsafe(site.api)).toEqual([]);
  });

  test('a click on the address already open only focuses it', async ({ page, context, site }) => {
    await controlled(page, `${site.url}/next/drafts/7`);
    await expect(card(page)).toContainText('Shared garden plans');
    const opened = page.url();
    const loads: string[] = [];
    page.on('framenavigated', (frame) => loads.push(frame.url()));
    await click(await workerAt(context, '/next/sw.js'), opened);
    await expect(card(page)).toContainText('Shared garden plans');
    expect(loads).toEqual([]);
    expect(unsafe(site.api)).toEqual([]);
  });
});

test.describe('a pushed address', () => {
  for (const path of ['/next/drafts/7', '/next/inbox/draft:7']) {
    test(`${path} resolves to the card`, async ({ page, site }) => {
      await page.goto(`${site.url}${path}`);
      await expect(page).toHaveURL(/\/next\/inbox\/draft(:|%3A)7$/);
      await expect(card(page)).toContainText('Shared garden plans');
      expect(unsafe(site.api)).toEqual([]);
    });
  }

  test('an expired item says nothing waits there', async ({ page, site }) => {
    await page.goto(`${site.url}/next/proposals/99`);
    await expect(page.getByRole('heading', { name: 'Nothing waits here' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Back to the Inbox' })).toBeVisible();
    expect(unsafe(site.api)).toEqual([]);
  });

  test('signed out, it asks for sign-in and then opens the card', async ({ page, site }) => {
    site.api.signedIn = false;
    site.api.loginUrl = '/login?reason=expired';
    await page.goto(`${site.url}/next/drafts/7`);
    await expect(page).toHaveURL(/\/login\?reason=expired&next=%2Fnext%2F/);
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
    const next = new URL(page.url()).searchParams.get('next');
    expect(next).toMatch(/^\/next\/(drafts\/7|inbox\/draft(:|%3A)7)$/);
    site.api.signedIn = true;
    await page.goto(`${site.url}${next}`);
    await expect(page).toHaveURL(/\/next\/inbox\/draft(:|%3A)7$/);
    await expect(card(page)).toContainText('Shared garden plans');
    expect(unsafe(site.api)).toEqual([]);
  });
});

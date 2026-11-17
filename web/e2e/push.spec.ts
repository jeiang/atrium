import { readFileSync } from 'node:fs';
import type { IncomingMessage, ServerResponse } from 'node:http';
import { resolve } from 'node:path';
import type { BrowserContext, Page, Worker } from '@playwright/test';
import type { Draft } from '../src/contract/index.ts';
import { type Api, fakeApi, signedInApi } from './api.ts';
import { test as base, expect } from './fixtures.ts';
import { serve } from './server.ts';

const dist = resolve(process.env.ATRIUM_WEB_DIST ?? 'dist');
const classicWorker = readFileSync(resolve('e2e/classic-sw.js'), 'utf8').replace('__ATRIUM_ASSETS__', '{}');
const nextWorker = [
  "self.addEventListener('install', (event) => event.waitUntil(self.skipWaiting()));",
  "self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()));",
].join('\n');
const html = 'text/html; charset=utf-8';
const script = 'text/javascript; charset=utf-8';

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

interface Device {
  id: number;
  endpoint: string;
}

interface Site {
  url: string;
  api: Api;
  moved: boolean;
  devices: Device[];
  registered: unknown[];
  updates: number;
}

const pushKey = 'BOrchardFictionalVapidKey2219abc';
const pushStore = 'atrium.e2e.push';

function devices(site: Site, req: IncomingMessage, res: ServerResponse, pathname: string): boolean {
  const method = req.method ?? 'GET';
  const json = (status: number, body?: unknown) => {
    res.writeHead(status, { 'Content-Type': 'application/json' }).end(body === undefined ? '' : JSON.stringify(body));
  };
  if (pathname === '/web/v1/notifications' && method === 'GET') {
    json(200, { push: { devices: site.devices } });
    return true;
  }
  if (pathname === '/web/v1/devices' && method === 'POST') {
    site.api.calls.push('POST /devices');
    const chunks: Buffer[] = [];
    req.on('data', (chunk: Buffer) => chunks.push(chunk));
    req.on('end', () => {
      const body = JSON.parse(Buffer.concat(chunks).toString('utf8')) as Device;
      site.registered.push(body);
      site.devices.push({ id: 42, endpoint: body.endpoint });
      json(201, { id: 42, endpoint: body.endpoint, label: 'This browser' });
    });
    return true;
  }
  const gone = /^\/web\/v1\/devices\/(\d+)$/.exec(pathname);
  if (gone && method === 'DELETE') {
    site.api.calls.push(`DELETE /devices/${gone[1]}`);
    site.devices = site.devices.filter((item) => item.id !== Number(gone[1]));
    json(204);
    return true;
  }
  return false;
}

function fakePush(store: string): void {
  const read = (): Record<string, { endpoint: string; key: string }> => JSON.parse(localStorage.getItem(store) ?? '{}');
  const write = (all: Record<string, { endpoint: string; key: string }>) =>
    localStorage.setItem(store, JSON.stringify(all));
  const encode = (buffer: ArrayBuffer) =>
    btoa(String.fromCharCode(...new Uint8Array(buffer)))
      .replace(/\+/g, '-')
      .replace(/\//g, '_')
      .replace(/=+$/, '');
  const decode = (text: string) =>
    Uint8Array.from(atob(text.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0)).buffer;
  const made = (scope: string, saved: { endpoint: string; key: string }) => ({
    endpoint: saved.endpoint,
    options: { userVisibleOnly: true, applicationServerKey: decode(saved.key) },
    getKey: (name: string) => decode(name === 'auth' ? 'YXV0aA' : 'cDI1NmRo'),
    unsubscribe: async () => {
      const all = read();
      delete all[scope];
      write(all);
      return true;
    },
  });
  Object.defineProperty(ServiceWorkerRegistration.prototype, 'pushManager', {
    configurable: true,
    get(this: ServiceWorkerRegistration) {
      const scope = this.scope;
      return {
        getSubscription: async () => {
          const saved = read()[scope];
          return saved ? made(scope, saved) : null;
        },
        subscribe: async (options: { applicationServerKey: ArrayBuffer | ArrayBufferView }) => {
          const key = options.applicationServerKey;
          const buffer =
            key instanceof ArrayBuffer ? key : key.buffer.slice(key.byteOffset, key.byteOffset + key.byteLength);
          const all = read();
          all[scope] = {
            endpoint: `https://push.example.test${new URL(scope).pathname}device`,
            key: encode(buffer as ArrayBuffer),
          };
          write(all);
          return made(scope, all[scope]);
        },
      };
    },
  });
  if ('Notification' in globalThis) Object.defineProperty(Notification, 'permission', { get: () => 'granted' });
}

function oldPage(register: string, scope: string): string {
  return `<!doctype html><title>Old page</title><script>navigator.serviceWorker.register('${register}', { scope: '${scope}' });</script>`;
}

function beforeMove(site: Site, res: ServerResponse, pathname: string): boolean {
  const pages: Record<string, [string, string]> = {
    '/old': [html, oldPage('/static/sw.js', '/')],
    '/plain': [html, '<!doctype html><title>Plain page</title>'],
    '/static/sw.js': [script, classicWorker],
  };
  if (site.moved && pathname === '/next/sw.js') site.updates += 1;
  if (!site.moved && pathname === '/next/sw.js') pages[pathname] = [script, nextWorker];
  else if (!site.moved && pathname.startsWith('/next/')) pages[pathname] = [html, oldPage('/next/sw.js', '/next/')];
  const page = pages[pathname];
  if (!page) return false;
  const headers: Record<string, string> = { 'Content-Type': page[0], 'Cache-Control': 'no-cache' };
  if (pathname === '/static/sw.js') headers['Service-Worker-Allowed'] = '/';
  res.writeHead(200, headers).end(page[1]);
  return true;
}

const test = base.extend<{ site: Site }>({
  site: async ({ browserName: _ }, use) => {
    const api: Api = { ...signedInApi(), drafts: [draft] };
    const handle = fakeApi(api);
    const site: Site = { url: '', api, moved: true, devices: [], registered: [], updates: 0 };
    const { url, server } = await serve(dist, {
      handle: (req: IncomingMessage, res, pathname) =>
        beforeMove(site, res, pathname) || devices(site, req, res, pathname) || handle(req, res, pathname),
    });
    site.url = url;
    await use(site);
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

function atRoot(site: Site, path: RegExp): RegExp {
  return new RegExp(`^${site.url.replaceAll('.', '\\.')}${path.source}$`);
}

const cardAddress = /\/inbox\/draft(:|%3A)7/;

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

function registrations(page: Page): Promise<{ scope: string; script: string }[]> {
  return page.evaluate(async () =>
    (await navigator.serviceWorker.getRegistrations()).map((item) => ({
      scope: new URL(item.scope).pathname,
      script: item.active ? new URL(item.active.scriptURL).pathname : '',
    })),
  );
}

async function registerNext(page: Page, site: Site): Promise<void> {
  await page.goto(`${site.url}/plain`);
  await page.evaluate(async () => {
    const registration = await navigator.serviceWorker.register('/next/sw.js', { scope: '/next/' });
    const worker = registration.installing ?? registration.waiting ?? registration.active;
    if (worker && worker.state !== 'activated') {
      const { promise, resolve: done } = Promise.withResolvers<void>();
      worker.addEventListener('statechange', () => worker.state === 'activated' && done());
      await promise;
    }
  });
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

test.describe('the move to the root address', () => {
  test('the classic and /next/ workers give way to one root worker', async ({ page, site }) => {
    site.moved = false;
    await page.goto(`${site.url}/old`);
    await expect.poll(() => registrations(page)).toEqual([{ scope: '/', script: '/static/sw.js' }]);
    await registerNext(page, site);
    expect(await registrations(page)).toEqual(
      expect.arrayContaining([
        { scope: '/', script: '/static/sw.js' },
        { scope: '/next/', script: '/next/sw.js' },
      ]),
    );
    site.moved = true;
    await page.goto(`${site.url}/`);
    await expect(page.getByRole('button', { name: 'Account: Ada Operator' })).toBeVisible();
    await expect.poll(() => registrations(page), { timeout: 15_000 }).toEqual([{ scope: '/', script: '/sw.js' }]);
    expect(unsafe(site.api)).toEqual([]);
  });

  test('a /next/ registration updates, re-subscribes push at root without asking and unregisters', async ({
    page,
    site,
  }) => {
    await page.context().addInitScript(fakePush, pushStore);
    site.moved = false;
    await registerNext(page, site);
    const before = await page.evaluate(
      async ({ key, store }) => {
        const legacy = await navigator.serviceWorker.getRegistration('/next/');
        const padded = key.replace(/-/g, '+').replace(/_/g, '/');
        const bytes = Uint8Array.from(atob(padded), (c) => c.charCodeAt(0));
        await legacy?.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: bytes });
        return JSON.parse(localStorage.getItem(store) ?? '{}') as Record<string, { endpoint: string; key: string }>;
      },
      { key: pushKey, store: pushStore },
    );
    const old = before[`${site.url}/next/`];
    expect(old?.endpoint).toBe('https://push.example.test/next/device');
    site.devices = [{ id: 41, endpoint: old?.endpoint ?? '' }];
    site.moved = true;
    await page.evaluate(async () => {
      const legacy = await navigator.serviceWorker.getRegistration('/next/');
      Reflect.set(globalThis, 'firstWorker', legacy?.active);
      await legacy?.update();
    });
    await expect
      .poll(() =>
        page.evaluate(async () => {
          const legacy = await navigator.serviceWorker.getRegistration('/next/');
          return (
            Boolean(legacy?.active) &&
            legacy?.active !== Reflect.get(globalThis, 'firstWorker') &&
            legacy?.active?.state
          );
        }),
      )
      .toBe('activated');
    expect(site.updates).toBeGreaterThan(0);
    expect(await registrations(page)).toEqual([{ scope: '/next/', script: '/next/sw.js' }]);
    await page.goto(`${site.url}/next/`);
    await expect(page).toHaveURL(`${site.url}/`);
    await expect(page.getByRole('button', { name: 'Account: Ada Operator' })).toBeVisible();
    await expect.poll(() => registrations(page), { timeout: 15_000 }).toEqual([{ scope: '/', script: '/sw.js' }]);
    const after = await page.evaluate(
      (store) => JSON.parse(localStorage.getItem(store) ?? '{}') as Record<string, { endpoint: string; key: string }>,
      pushStore,
    );
    expect(Object.keys(after)).toEqual([`${site.url}/`]);
    expect(after[`${site.url}/`]).toEqual({ endpoint: 'https://push.example.test/device', key: old?.key });
    expect(site.registered).toEqual([
      { endpoint: 'https://push.example.test/device', p256dh: 'cDI1NmRo', auth: 'YXV0aA' },
    ]);
    expect(site.devices).toEqual([{ id: 42, endpoint: 'https://push.example.test/device' }]);
    expect(unsafe(site.api)).toEqual(['POST /devices', 'DELETE /devices/41']);
  });

  test('a classic worker at / keeps its push subscription when /sw.js replaces it', async ({ page, site }) => {
    await page.context().addInitScript(fakePush, pushStore);
    site.moved = false;
    await page.goto(`${site.url}/old`);
    await expect.poll(() => registrations(page)).toEqual([{ scope: '/', script: '/static/sw.js' }]);
    const before = await page.evaluate(
      async ({ key, store }) => {
        const classic = await navigator.serviceWorker.getRegistration('/');
        const bytes = Uint8Array.from(atob(key.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0));
        await classic?.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: bytes });
        return localStorage.getItem(store);
      },
      { key: pushKey, store: pushStore },
    );
    site.devices = [{ id: 40, endpoint: 'https://push.example.test/device' }];
    site.moved = true;
    await page.goto(`${site.url}/`);
    await expect(page.getByRole('button', { name: 'Account: Ada Operator' })).toBeVisible();
    await expect.poll(() => registrations(page), { timeout: 15_000 }).toEqual([{ scope: '/', script: '/sw.js' }]);
    expect(await page.evaluate((store) => localStorage.getItem(store), pushStore)).toBe(before);
    expect(site.devices).toEqual([{ id: 40, endpoint: 'https://push.example.test/device' }]);
    expect(unsafe(site.api)).toEqual([]);
  });

  test('an open /next/ window moves to the same page at root when its worker updates', async ({ page, site }) => {
    site.moved = false;
    await page.goto(`${site.url}/next/inbox/draft:7`);
    await expect.poll(() => page.evaluate(() => Boolean(navigator.serviceWorker.controller))).toBe(true);
    site.moved = true;
    await page
      .evaluate(async () => {
        await (await navigator.serviceWorker.getRegistration())?.update();
      })
      .catch(() => undefined);
    await expect(page).toHaveURL(atRoot(site, cardAddress));
    await expect(card(page)).toContainText('Shared garden plans');
    await expect
      .poll(() => registrations(page), { timeout: 15_000 })
      .not.toContainEqual(expect.objectContaining({ scope: '/next/' }));
    expect(unsafe(site.api)).toEqual([]);
  });

  for (const path of ['/next/drafts/7', '/next/inbox/draft:7']) {
    test(`${path} ends on the card at root`, async ({ page, site }) => {
      await page.goto(`${site.url}${path}`);
      await expect(page).toHaveURL(atRoot(site, cardAddress));
      await expect(card(page)).toContainText('Shared garden plans');
    });
  }

  test('/next/ ends on Home at root with its query', async ({ page, site }) => {
    await page.goto(`${site.url}/next/?from=bookmark`);
    await expect(page).toHaveURL(atRoot(site, /\/\?from=bookmark/));
    await expect(page.getByRole('button', { name: 'Account: Ada Operator' })).toBeVisible();
  });
});

test.describe('a push notification click', () => {
  test.skip(({ browserName }) => browserName !== 'chromium', 'Playwright reaches service workers only in Chromium');

  for (const sent of ['/drafts/7', '/next/drafts/7', '/inbox/draft:7']) {
    test(`${sent} opens the card at root in the open window and sends no write`, async ({ page, context, site }) => {
      await controlled(page, `${site.url}/`);
      await click(await workerAt(context, '/sw.js'), sent);
      await expect(page).toHaveURL(atRoot(site, cardAddress));
      await expect(card(page)).toContainText('Shared garden plans');
      expect(unsafe(site.api)).toEqual([]);
    });
  }

  test('a decided or unknown item lands on the Inbox with a plain message', async ({ page, context, site }) => {
    await controlled(page, `${site.url}/`);
    await click(await workerAt(context, '/sw.js'), '/next/proposals/99');
    await expect(page).toHaveURL(atRoot(site, /\/inbox\/proposal(:|%3A)99/));
    await expect(page.getByRole('heading', { name: 'Nothing waits here' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Back to the Inbox' })).toBeVisible();
    expect(unsafe(site.api)).toEqual([]);
  });

  test('a notification from a /next/ worker not yet retired focuses the card already open at root', async ({
    page,
    context,
    site,
  }) => {
    await page.goto(`${site.url}/drafts/7`);
    await expect(page).toHaveURL(atRoot(site, cardAddress));
    await expect(card(page)).toContainText('Shared garden plans');
    await expect.poll(() => registrations(page)).toEqual([{ scope: '/', script: '/sw.js' }]);
    const helper = await context.newPage();
    await registerNext(helper, site);
    await helper.close();
    const loads: string[] = [];
    page.on('framenavigated', (frame) => loads.push(frame.url()));
    await click(await workerAt(context, '/next/sw.js'), `/next${new URL(page.url()).pathname}`);
    await expect(card(page)).toContainText('Shared garden plans');
    expect(loads).toEqual([]);
    expect(context.pages()).toHaveLength(1);
    expect(unsafe(site.api)).toEqual([]);
  });

  test('a click on the address already open only focuses it', async ({ page, context, site }) => {
    await controlled(page, `${site.url}/drafts/7`);
    await expect(card(page)).toContainText('Shared garden plans');
    const opened = page.url();
    const loads: string[] = [];
    page.on('framenavigated', (frame) => loads.push(frame.url()));
    await click(await workerAt(context, '/sw.js'), opened);
    await expect(card(page)).toContainText('Shared garden plans');
    expect(loads).toEqual([]);
    expect(unsafe(site.api)).toEqual([]);
  });
});

test.describe('a pushed address', () => {
  for (const path of ['/drafts/7', '/inbox/draft:7']) {
    test(`${path} resolves to the card`, async ({ page, site }) => {
      await page.goto(`${site.url}${path}`);
      await expect(page).toHaveURL(atRoot(site, cardAddress));
      await expect(card(page)).toContainText('Shared garden plans');
      expect(unsafe(site.api)).toEqual([]);
    });
  }

  test('an expired item says nothing waits there', async ({ page, site }) => {
    await page.goto(`${site.url}/proposals/99`);
    await expect(page.getByRole('heading', { name: 'Nothing waits here' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Back to the Inbox' })).toBeVisible();
    expect(unsafe(site.api)).toEqual([]);
  });

  test('signed out, it asks for sign-in and then opens the card', async ({ page, site }) => {
    site.api.signedIn = false;
    site.api.loginUrl = '/login?reason=expired';
    await page.goto(`${site.url}/drafts/7`);
    await expect(page).toHaveURL(/\/login\?reason=expired&next=%2F/);
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
    const next = new URL(page.url()).searchParams.get('next');
    expect(next).toMatch(/^\/(drafts\/7|inbox\/draft(:|%3A)7)$/);
    site.api.signedIn = true;
    await page.goto(`${site.url}${next}`);
    await expect(page).toHaveURL(atRoot(site, cardAddress));
    await expect(card(page)).toContainText('Shared garden plans');
    expect(unsafe(site.api)).toEqual([]);
  });
});

declare const PRECACHE: readonly string[];
declare const VERSION: string;

const worker = self as unknown as ServiceWorkerGlobalScope;
const ROOT = '/';
const SCOPE = new URL(worker.registration.scope).pathname;
const LEGACY = SCOPE !== ROOT;
const SHELL = PRECACHE[0] ?? ROOT;
const PREFIX = 'shell-';
const OLD = ['next-shell-', 'atrium-'];
const CACHE = `${PREFIX}${VERSION}`;
const NAVIGATION_TIMEOUT_MS = 3_000;
const SERVER =
  /^\/(?:login|logout|auth|api|web|healthz|media|next)(?:\/|$)|^\/apps\/media\/file\/|^\/settings\/connections\/google\/callback\/?$|^\/third-party-notices\.txt$/;
const precached = new Set(PRECACHE);

async function precache(): Promise<void> {
  const cache = await caches.open(CACHE);
  await Promise.all(
    PRECACHE.map(async (url) => {
      const reused = url === SHELL ? undefined : await caches.match(url);
      const response = reused ?? (await fetch(new Request(url, { cache: 'no-cache', credentials: 'same-origin' })));
      if (!response.ok) throw new Error(`precache ${url}: HTTP ${response.status}`);
      await cache.put(url, response);
    }),
  );
}

async function prune(): Promise<void> {
  const keys = await caches.keys();
  const stale = (key: string) =>
    OLD.some((prefix) => key.startsWith(prefix)) || (!LEGACY && key.startsWith(PREFIX) && key !== CACHE);
  await Promise.all(keys.filter(stale).map((key) => caches.delete(key)));
}

function atRoot(href: string): string {
  const url = new URL(href);
  if (url.pathname !== '/next' && !url.pathname.startsWith('/next/')) return url.href;
  return new URL(`${url.pathname.slice('/next'.length) || ROOT}${url.search}${url.hash}`, url.origin).href;
}

async function leave(): Promise<void> {
  await prune();
  await worker.clients.claim();
  const windows = await worker.clients.matchAll({ type: 'window' });
  await Promise.all(
    windows
      .filter((client) => new URL(client.url).pathname.startsWith(SCOPE))
      .map((client) => client.navigate(atRoot(client.url)).catch(() => undefined)),
  );
}

async function asset(request: Request, pathname: string): Promise<Response> {
  const cached = await caches
    .open(CACHE)
    .then((cache) => cache.match(pathname))
    .catch(() => undefined);
  return cached ?? fetch(request).catch(() => Response.error());
}

async function shell(): Promise<Response | undefined> {
  const cache = await caches.open(CACHE);
  return cache.match(SHELL);
}

async function navigate(request: Request): Promise<Response> {
  const network = fetch(request);
  const answer = network.then(
    (response) => (response.status >= 500 ? undefined : response),
    () => undefined,
  );
  const late = Promise.withResolvers<undefined>();
  const timer = setTimeout(() => late.resolve(undefined), NAVIGATION_TIMEOUT_MS);
  const response = await Promise.race([answer, late.promise]);
  clearTimeout(timer);
  if (response) return response;
  return (await shell()) ?? network;
}

if (LEGACY) {
  worker.addEventListener('install', (event) => {
    event.waitUntil(worker.skipWaiting());
  });

  worker.addEventListener('activate', (event) => {
    event.waitUntil(leave());
  });
} else {
  worker.addEventListener('install', (event) => {
    event.waitUntil(precache());
  });

  worker.addEventListener('activate', (event) => {
    event.waitUntil(prune());
  });

  worker.addEventListener('message', (event) => {
    const data: unknown = event.data;
    if (typeof data !== 'object' || data === null || !('type' in data) || data.type !== 'activate') return;
    const urls: unknown[] | undefined = 'urls' in data && Array.isArray(data.urls) ? data.urls : undefined;
    const current =
      urls === undefined ||
      (urls.length > 0 &&
        urls.every((url) => typeof url === 'string' && precached.has(new URL(url, worker.location.href).pathname)));
    if (current) {
      event.waitUntil(worker.skipWaiting());
    } else {
      event.source?.postMessage({ type: 'outdated' });
    }
  });

  worker.addEventListener('fetch', (event) => {
    const { request } = event;
    if (request.method !== 'GET') return;
    const url = new URL(request.url);
    if (url.origin !== worker.location.origin || SERVER.test(url.pathname)) return;
    if (url.pathname.startsWith('/assets/')) {
      if (precached.has(url.pathname)) event.respondWith(asset(request, url.pathname));
    } else if (request.mode === 'navigate') {
      event.respondWith(navigate(request));
    }
  });
}

function sameOrigin(value: unknown): string {
  const home = new URL(ROOT, worker.location.origin).href;
  try {
    const url = new URL(typeof value === 'string' && value ? value : ROOT, worker.location.origin);
    return url.origin === worker.location.origin ? url.href : home;
  } catch {
    return home;
  }
}

worker.addEventListener('push', (event) => {
  let data: { title?: unknown; body?: unknown; url?: unknown } = {};
  try {
    data = event.data?.json() ?? {};
  } catch {
    data = {};
  }
  event.waitUntil(
    worker.registration.showNotification(typeof data.title === 'string' && data.title ? data.title : 'atrium', {
      body: typeof data.body === 'string' ? data.body : '',
      data: { url: atRoot(sameOrigin(data.url)) },
    }),
  );
});

async function openTarget(target: string): Promise<unknown> {
  const windows = await worker.clients.matchAll({ type: 'window', includeUncontrolled: true });
  const exact = windows.find((client) => client.url === target);
  if (exact) return exact.focus();
  const own = await worker.clients.matchAll({ type: 'window' });
  const near = own.find((client) => client.focused) ?? own[0];
  if (!near) return worker.clients.openWindow(target);
  await near.focus().catch(() => undefined);
  return near.navigate(target);
}

worker.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const data: unknown = event.notification.data;
  event.waitUntil(
    openTarget(atRoot(sameOrigin(data && typeof data === 'object' && 'url' in data ? data.url : undefined))),
  );
});

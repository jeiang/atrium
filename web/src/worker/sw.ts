declare const PRECACHE: readonly string[];
declare const VERSION: string;

const worker = self as unknown as ServiceWorkerGlobalScope;
const BASE = new URL(worker.registration.scope).pathname;
const SHELL = PRECACHE[0] ?? BASE;
const PREFIX = 'next-shell-';
const CACHE = `${PREFIX}${VERSION}`;
const NAVIGATION_TIMEOUT_MS = 3_000;
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
  await Promise.all(keys.filter((key) => key.startsWith(PREFIX) && key !== CACHE).map((key) => caches.delete(key)));
}

async function asset(request: Request): Promise<Response> {
  const cache = await caches.open(CACHE);
  return (await cache.match(request)) ?? fetch(request);
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
    (urls.length > 0 && urls.every((url) => typeof url === 'string' && precached.has(new URL(url, worker.location.href).pathname)));
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
  if (url.origin !== worker.location.origin || !url.pathname.startsWith(BASE)) return;
  if (url.pathname.startsWith(`${BASE}assets/`)) {
    event.respondWith(asset(request));
  } else if (request.mode === 'navigate') {
    event.respondWith(navigate(request));
  }
});

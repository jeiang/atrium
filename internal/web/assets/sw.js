const ASSETS = __ATRIUM_ASSETS__;
const CACHE = `atrium-${Object.values(ASSETS).join("|")}`;
const PRECACHE = Object.entries(ASSETS).filter(([name]) => /\.(css|js|woff2)$/.test(name)).map(([, path]) => path);
const home = () => new URL("/", self.location.origin).href;

const local = (value) => {
  try {
    const url = new URL(value || "/", self.location.origin);
    return url.origin === self.location.origin ? url.href : home();
  } catch {
    return home();
  }
};

const open = async (target) => {
  const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
  const same = windows.find((client) => client.url === target);
  if (same) return same.focus();
  const own = await self.clients.matchAll({ type: "window" });
  const near = own.find((client) => client.focused) || own[0];
  if (!near) return self.clients.openWindow(target);
  await near.focus().catch(() => undefined);
  return near.navigate(target);
};

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(CACHE).then((cache) => cache.addAll(PRECACHE)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (event) => {
  event.waitUntil(caches.keys()
    .then((keys) => Promise.all(keys.filter((key) => key.startsWith("atrium-") && key !== CACHE).map((key) => caches.delete(key))))
    .then(() => self.clients.claim()));
});

self.addEventListener("fetch", (event) => {
  const url = new URL(event.request.url);
  if (event.request.method !== "GET" || url.origin !== self.location.origin || !PRECACHE.includes(url.pathname)) return;
  event.respondWith(caches.match(url.pathname).then((cached) => cached || fetch(event.request)));
});

self.addEventListener("push", (event) => {
  let data = {};
  try {
    data = event.data ? event.data.json() : {};
  } catch {
    data = {};
  }
  event.waitUntil(self.registration.showNotification(data.title || "atrium", {
    body: data.body || "",
    data: { url: local(data.url) },
  }));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const { url } = event.notification.data || {};
  event.waitUntil(open(url || home()));
});

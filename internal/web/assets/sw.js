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
  return self.clients.openWindow(target);
};

self.addEventListener("install", () => self.skipWaiting());

self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));

self.addEventListener("push", (event) => {
  let data = {};
  try {
    data = event.data ? event.data.json() : {};
  } catch {
    data = {};
  }
  const actions = Array.isArray(data.actions) ? data.actions.slice(0, 2) : [];
  event.waitUntil(self.registration.showNotification(data.title || "atrium", {
    body: data.body || "",
    data: { url: local(data.url), actions: Object.fromEntries(actions.map((action) => [action.action, local(action.url)])) },
    actions: actions.map((action) => ({ action: action.action, title: action.title })),
  }));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const { url, actions } = event.notification.data || {};
  const target = url || home();
  const post = event.action && actions ? actions[event.action] : "";
  if (!post) {
    event.waitUntil(open(target));
    return;
  }
  event.waitUntil(fetch(post, { method: "POST", credentials: "same-origin" })
    .then((response) => (response.ok ? undefined : open(target)))
    .catch(() => open(target)));
});

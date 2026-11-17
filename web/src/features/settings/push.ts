import { notificationsConfiguration, notificationsSubscribe, notificationsUnsubscribe, ok } from '../../contract';
import { appWorker, legacyWorker } from '../../shared/worker';

export type Permission = NotificationPermission | 'unknown';

export interface DeviceState {
  support: 'ready' | 'install' | 'unsupported';
  permission: Permission;
  endpoint?: string;
  legacy?: string;
}

export interface Subscribed {
  endpoint: string;
  p256dh: string;
  auth: string;
}

async function pushWorker(): Promise<ServiceWorkerRegistration | undefined> {
  const registration = await appWorker;
  if (!registration || !('PushManager' in globalThis) || !('pushManager' in registration)) return undefined;
  return registration;
}

async function activated(registration: ServiceWorkerRegistration): Promise<void> {
  if (registration.active) return;
  const pending = registration.installing ?? registration.waiting;
  if (!pending) return;
  const { promise, resolve } = Promise.withResolvers<void>();
  pending.addEventListener('statechange', () => {
    if (pending.state === 'activated' || pending.state === 'redundant') resolve();
  });
  await promise;
}

function bytes(text: string): Uint8Array<ArrayBuffer> {
  const padded = text.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (text.length % 4)) % 4);
  return Uint8Array.from(atob(padded), (c) => c.charCodeAt(0));
}

function text(buffer: ArrayBuffer | null): string {
  if (!buffer) return '';
  return btoa(String.fromCharCode(...new Uint8Array(buffer)))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '');
}

export async function readDevice(): Promise<DeviceState> {
  const registration = await pushWorker();
  const permission: Permission = 'Notification' in globalThis ? Notification.permission : 'unknown';
  if (!registration) {
    const homeScreen = Reflect.get(navigator, 'standalone') === false && navigator.maxTouchPoints > 0;
    return { support: homeScreen ? 'install' : 'unsupported', permission };
  }
  const own = await registration.pushManager.getSubscription();
  const legacy = await legacyWorker();
  const old = legacy && 'pushManager' in legacy ? await legacy.pushManager.getSubscription() : null;
  return {
    support: 'ready',
    permission,
    ...(own ? { endpoint: own.endpoint } : {}),
    ...(old ? { legacy: old.endpoint } : {}),
  };
}

export async function subscribe(key: string): Promise<Subscribed> {
  const registration = await pushWorker();
  if (!registration) throw new Error('This browser cannot receive push.');
  await activated(registration);
  const wanted = bytes(key);
  let subscription = await registration.pushManager.getSubscription();
  const current = subscription?.options.applicationServerKey;
  if (subscription && (!current || text(current) !== text(wanted.buffer))) {
    await subscription.unsubscribe();
    subscription = null;
  }
  subscription ??= await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: wanted });
  return {
    endpoint: subscription.endpoint,
    p256dh: text(subscription.getKey('p256dh')),
    auth: text(subscription.getKey('auth')),
  };
}

export async function unsubscribe(endpoint: string): Promise<void> {
  const registration = await pushWorker();
  if (!registration) return;
  const legacy = await legacyWorker();
  for (const worker of legacy && 'pushManager' in legacy ? [registration, legacy] : [registration]) {
    const subscription = await worker.pushManager.getSubscription();
    if (subscription?.endpoint === endpoint) await subscription.unsubscribe();
  }
}

export async function handleLegacyWorker(
  root: ServiceWorkerRegistration,
  legacy: ServiceWorkerRegistration,
): Promise<void> {
  const old = 'pushManager' in legacy ? await legacy.pushManager.getSubscription() : null;
  if (old) {
    const key = old.options.applicationServerKey;
    if (!key || !('pushManager' in root) || !('Notification' in globalThis) || Notification.permission !== 'granted') {
      throw new Error('the legacy subscription cannot move without a prompt');
    }
    await activated(root);
    let current = await root.pushManager.getSubscription();
    const held = current?.options.applicationServerKey;
    if (current && (!held || text(held) !== text(key))) throw new Error('this device holds a different push key');
    current ??= await root.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
    const made: Subscribed = {
      endpoint: current.endpoint,
      p256dh: text(current.getKey('p256dh')),
      auth: text(current.getKey('auth')),
    };
    ok(await notificationsSubscribe(made, { headers: { 'Idempotency-Key': crypto.randomUUID() } }));
    if (old.endpoint !== made.endpoint) {
      const devices = ok(await notificationsConfiguration()).push.devices;
      const stale = devices.find((item) => item.endpoint === old.endpoint);
      if (stale) {
        ok(await notificationsUnsubscribe(stale.id, { headers: { 'Idempotency-Key': crypto.randomUUID() } }));
      }
      if (!(await old.unsubscribe())) throw new Error('the browser kept the legacy subscription');
    }
  }
  if (!(await legacy.unregister())) throw new Error('the browser kept the legacy registration');
}

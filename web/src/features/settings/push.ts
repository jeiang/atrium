import { appWorker } from '../../shared/worker';

export type Permission = NotificationPermission | 'unknown';

export interface DeviceState {
  support: 'ready' | 'install' | 'unsupported';
  permission: Permission;
  endpoint?: string;
  classic?: string;
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

async function classicWorker(own: ServiceWorkerRegistration): Promise<ServiceWorkerRegistration | undefined> {
  const root = await navigator.serviceWorker.getRegistration('/').catch(() => undefined);
  if (!root || root.scope === own.scope || new URL(root.scope).pathname !== '/' || !('pushManager' in root)) {
    return undefined;
  }
  return root;
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
    const standalone: unknown = Reflect.get(navigator, 'standalone');
    return { support: standalone === false ? 'install' : 'unsupported', permission };
  }
  const own = await registration.pushManager.getSubscription();
  const classic = await classicWorker(registration);
  const old = classic ? await classic.pushManager.getSubscription() : null;
  return {
    support: 'ready',
    permission,
    ...(own ? { endpoint: own.endpoint } : {}),
    ...(old ? { classic: old.endpoint } : {}),
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
  const classic = await classicWorker(registration);
  for (const worker of classic ? [registration, classic] : [registration]) {
    const subscription = await worker.pushManager.getSubscription();
    if (subscription?.endpoint === endpoint) await subscription.unsubscribe();
  }
}

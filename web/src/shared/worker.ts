interface ScriptUrlPolicy {
  createScriptURL(input: string): string;
}

interface TrustedTypePolicyFactory {
  createPolicy(name: string, rules: { createScriptURL(input: string): string }): ScriptUrlPolicy;
}

const BASE = import.meta.env.BASE_URL;
const SCRIPT = `${BASE}sw.js`;
const LEGACY_SCOPE = '/next/';
const ACTIVATION_WAIT_MS = 3_000;

let stale = false;
const registered = Promise.withResolvers<ServiceWorkerRegistration | undefined>();

export const appWorker = registered.promise;

export function useWorker(registration: ServiceWorkerRegistration | undefined): void {
  registered.resolve(registration);
}

function scriptURL(): string {
  const factory: TrustedTypePolicyFactory | undefined = Reflect.get(globalThis, 'trustedTypes');
  if (!factory) return SCRIPT;
  const policy = factory.createPolicy('atrium-sw', {
    createScriptURL(input) {
      if (input !== SCRIPT) throw new TypeError(`atrium-sw refuses ${input}`);
      return input;
    },
  });
  return policy.createScriptURL(SCRIPT);
}

function pageAssets(): string[] {
  const nodes = document.querySelectorAll<HTMLScriptElement | HTMLLinkElement>(
    'script[src], link[rel="stylesheet"][href]',
  );
  return [...nodes].map((node) => new URL('src' in node ? node.src : node.href).pathname);
}

function offerActivation(waiting: ServiceWorker | null): void {
  waiting?.postMessage({ type: 'activate', urls: pageAssets() });
}

async function activateThen(registration: ServiceWorkerRegistration, go: () => void): Promise<void> {
  const waiting = registration.waiting;
  if (waiting) {
    const { promise, resolve } = Promise.withResolvers<void>();
    const timer = setTimeout(resolve, ACTIVATION_WAIT_MS);
    navigator.serviceWorker.addEventListener('controllerchange', () => resolve(), { once: true });
    waiting.postMessage({ type: 'activate' });
    await promise;
    clearTimeout(timer);
  }
  go();
}

function watchNavigation(registration: ServiceWorkerRegistration): void {
  document.addEventListener(
    'click',
    (event) => {
      if (!stale || event.defaultPrevented || event.button !== 0) return;
      if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      const link = (event.target as Element | null)?.closest?.('a[href]');
      if (!(link instanceof HTMLAnchorElement) || link.target || link.hasAttribute('download')) return;
      const url = new URL(link.href);
      if (url.origin !== location.origin || !url.pathname.startsWith(BASE)) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      void activateThen(registration, () => location.assign(url.href));
    },
    true,
  );
  addEventListener('popstate', () => {
    if (stale) void activateThen(registration, () => location.reload());
  });
}

export async function legacyWorker(): Promise<ServiceWorkerRegistration | undefined> {
  const all = await navigator.serviceWorker.getRegistrations().catch(() => []);
  return all.find((item) => new URL(item.scope).pathname === LEGACY_SCOPE);
}

async function retireLegacy(registration: ServiceWorkerRegistration, signedIn: Promise<boolean>): Promise<void> {
  const legacy = await legacyWorker();
  if (!legacy || !(await signedIn)) return;
  try {
    const { handleLegacyWorker } = await import('../features/settings/push');
    await handleLegacyWorker(registration, legacy);
  } catch {
    await legacy.update().catch(() => undefined);
  }
}

export async function startWorker(signedIn: Promise<boolean>): Promise<void> {
  if (!('serviceWorker' in navigator)) {
    useWorker(undefined);
    return;
  }
  const container = navigator.serviceWorker;
  let registration: ServiceWorkerRegistration;
  try {
    registration = await container.register(scriptURL(), { scope: BASE, updateViaCache: 'none' });
  } catch {
    useWorker(undefined);
    return;
  }
  useWorker(registration);
  container.startMessages();
  container.addEventListener('message', (event: MessageEvent<unknown>) => {
    const data = event.data;
    if (typeof data === 'object' && data !== null && 'type' in data && data.type === 'outdated') stale = true;
  });
  container.addEventListener('controllerchange', () => offerActivation(container.controller));
  registration.addEventListener('updatefound', () => {
    const installing = registration.installing;
    installing?.addEventListener('statechange', () => {
      if (installing.state === 'installed' && container.controller) offerActivation(installing);
    });
  });
  watchNavigation(registration);
  if (registration.waiting && container.controller) offerActivation(registration.waiting);
  addEventListener('online', () => void registration.update().catch(() => undefined));
  await retireLegacy(registration, signedIn);
}

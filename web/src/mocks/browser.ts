import { setupWorker } from 'msw/browser';
import { handlers } from './handlers';

const WORKER_PATH = '/mockServiceWorker.js';

interface TrustedTypePolicyFactory {
  createPolicy(name: string, rules: { createScriptURL(input: string): string }): unknown;
  readonly defaultPolicy: unknown;
}

function allowWorkerUrl(input: string): string {
  const url = new URL(input, location.href);
  if (url.origin === location.origin && url.pathname === WORKER_PATH && !url.search && !url.hash) return input;
  throw new TypeError(`Trusted Types: script URL not allowed: ${input}`);
}

export async function startMocks(): Promise<ServiceWorkerRegistration | undefined> {
  const factory: TrustedTypePolicyFactory | undefined = Reflect.get(globalThis, 'trustedTypes');
  if (factory && !factory.defaultPolicy) factory.createPolicy('default', { createScriptURL: allowWorkerUrl });
  return setupWorker(...handlers).start({
    serviceWorker: { url: WORKER_PATH },
    onUnhandledFrame: 'bypass',
    quiet: true,
  });
}

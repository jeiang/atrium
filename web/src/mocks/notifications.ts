import { delay, HttpResponse, http } from 'msw';
import type { NotificationChannel, NotificationSettings, PushDevice } from '../contract';
import { API_BASE } from '../contract';

export interface NotificationsScenario {
  devices?: 'none';
  key?: 'missing';
  fail?: boolean;
  slow?: boolean;
  testFails?: boolean;
  subscribeFails?: 'refused' | 'lost';
}

export interface NotificationsRequest {
  method: string;
  path: string;
  key: string | null;
  body: unknown;
}

export const PUSH_KEY = 'BM3wXh0N9v4d2b8kQm1JtYcR7pL5sE6aF0uGzHqWnVx3yTiKoD8jCe4lB2rS9fA7gP1hU6mZ5wN0vX3cQ8tY2kE';
const DEVICE_LIMIT = 16;
const EVENTS = 'atrium-mock-events';
const DAY = 86_400;
const CONTENT = ['auto', 'full', 'title'];
const log = globalThis as typeof globalThis & { atriumNotifyRequests?: NotificationsRequest[] };

interface Store {
  channels: NotificationChannel[];
  devices: PushDevice[];
  next: number;
}

function now(): number {
  return Date.now() / 1000;
}

function seed(state: NotificationsScenario): Store {
  const devices: PushDevice[] =
    state.devices === 'none'
      ? []
      : [
          {
            id: 1,
            transport: 'webpush',
            endpoint: 'https://push.example.test/send/kitchen-tablet',
            label: 'Firefox on Linux',
            created_ts: now() - 40 * DAY,
            failure_count: 0,
            last_error: null,
            last_ok_ts: now() - DAY / 2,
          },
          {
            id: 2,
            transport: 'webpush',
            endpoint: 'https://push.example.test/send/old-phone',
            label: 'Safari on iOS',
            created_ts: now() - 120 * DAY,
            failure_count: 4,
            last_error: 'push service answered 410 Gone',
            last_ok_ts: now() - 30 * DAY,
          },
        ];
  return {
    channels: [
      { name: 'assistant', title: 'Assistant', content: 'auto', full_text: true },
      { name: 'media', title: 'Library', content: 'full', full_text: true },
      { name: 'system', title: 'System', content: 'title', full_text: false },
    ],
    devices,
    next: devices.length + 1,
  };
}

let store: Store = seed({});
let scenarioText = '';

function scenario(read: () => { notifications?: NotificationsScenario }): NotificationsScenario {
  const state = read().notifications ?? {};
  const text = JSON.stringify(state);
  if (text !== scenarioText) {
    scenarioText = text;
    store = seed(state);
  }
  return state;
}

function publish(): void {
  const channel = new BroadcastChannel(EVENTS);
  channel.postMessage({ topic: 'notify' });
  channel.close();
}

async function record(request: Request): Promise<unknown> {
  const text = await request.clone().text();
  let body: unknown;
  try {
    body = text ? JSON.parse(text) : undefined;
  } catch {
    body = text;
  }
  log.atriumNotifyRequests = [
    ...(log.atriumNotifyRequests ?? []),
    {
      method: request.method,
      path: new URL(request.url).pathname,
      key: request.headers.get('Idempotency-Key'),
      body,
    },
  ];
  return body;
}

function refusal(status: number, code: string, message: string, extra: Record<string, unknown> = {}): Response {
  return HttpResponse.json({ error: { code, message, ...extra } }, { status });
}

function invalid(field: string, message: string): Response {
  return refusal(422, 'invalid', message, { fields: [{ name: field, code: 'invalid', message }] });
}

function base64url(text: unknown): Uint8Array | undefined {
  if (typeof text !== 'string' || !/^[A-Za-z0-9_-]+$/.test(text)) return undefined;
  const padded = text.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (text.length % 4)) % 4);
  try {
    return Uint8Array.from(atob(padded), (c) => c.charCodeAt(0));
  } catch {
    return undefined;
  }
}

function pushLabel(agent: string): string {
  const browsers: [string, string][] = [
    ['Edg/', 'Edge'],
    ['Firefox/', 'Firefox'],
    ['FxiOS/', 'Firefox'],
    ['CriOS/', 'Chrome'],
    ['Chrome/', 'Chrome'],
    ['Safari/', 'Safari'],
  ];
  const systems: [string, string][] = [
    ['iPhone', 'iOS'],
    ['iPad', 'iPadOS'],
    ['Android', 'Android'],
    ['Mac OS X', 'macOS'],
    ['Windows', 'Windows'],
    ['CrOS', 'ChromeOS'],
    ['Linux', 'Linux'],
  ];
  const browser = browsers.find(([token]) => agent.includes(token))?.[1];
  const system = systems.find(([token]) => agent.includes(token))?.[1];
  if (browser && system) return `${browser} on ${system}`;
  return browser ?? system ?? 'Browser';
}

function settings(state: NotificationsScenario): NotificationSettings {
  return {
    channels: store.channels,
    error: '',
    push: {
      key: state.key === 'missing' ? '' : PUSH_KEY,
      problem: state.key === 'missing' ? 'webpush_subject is not set' : '',
      devices: store.devices,
    },
  };
}

export function notificationHandlers(read: () => { notifications?: NotificationsScenario }) {
  return [
    http.get(`*${API_BASE}/notifications`, async () => {
      const state = scenario(read);
      if (state.slow) await delay(1500);
      if (state.fail) return refusal(500, 'internal', 'The notification store did not answer.');
      return HttpResponse.json(settings(state), { headers: { ETag: `W/"n${store.next}"` } });
    }),
    http.put(`*${API_BASE}/notifications/channels/:channel`, async ({ request, params }) => {
      scenario(read);
      const body = (await record(request)) as { content?: unknown } | undefined;
      const channel = store.channels.find((item) => item.name === params.channel);
      if (!channel) return refusal(409, 'refused', `unknown channel ${String(params.channel)}`);
      const content = body?.content;
      if (typeof content !== 'string' || !CONTENT.includes(content)) {
        return refusal(409, 'refused', 'content must be auto, full or title');
      }
      store.channels = store.channels.map((item) =>
        item.name === channel.name ? { ...item, content, full_text: content !== 'title' } : item,
      );
      publish();
      return new HttpResponse(null, { status: 204 });
    }),
    http.post(`*${API_BASE}/devices/test`, async ({ request }) => {
      const state = scenario(read);
      await record(request);
      if (store.devices.length === 0) return refusal(409, 'refused', 'Test not delivered: no push devices');
      if (state.testFails) return refusal(409, 'refused', 'Test not delivered: push service answered 410 Gone');
      store.devices = store.devices.map((device) => ({ ...device, last_ok_ts: now(), failure_count: 0 }));
      publish();
      return HttpResponse.json({ sent: store.devices.length, failure: '', outcome: 'delivered' });
    }),
    http.post(`*${API_BASE}/devices`, async ({ request }) => {
      const state = scenario(read);
      const body = (await record(request)) as Record<string, unknown> | undefined;
      if (state.subscribeFails === 'lost') return HttpResponse.error();
      if (state.subscribeFails === 'refused') return refusal(409, 'refused', 'the notifications module is off');
      const endpoint = typeof body?.endpoint === 'string' ? body.endpoint.trim() : '';
      if (!/^https:\/\/[^/@]+/.test(endpoint)) return invalid('endpoint', 'push endpoint must be an https URL');
      if (!base64url(body?.p256dh)) return invalid('p256dh', 'p256dh must be base64url');
      if (base64url(body?.auth)?.length !== 16) return invalid('auth', 'auth must be 16 bytes of base64url');
      const label =
        typeof body?.label === 'string' && body.label.trim() ? body.label.trim() : pushLabel(navigator.userAgent);
      const known = store.devices.find((device) => device.endpoint === endpoint);
      if (known) {
        store.devices = store.devices.map((device) =>
          device.id === known.id ? { ...device, label, failure_count: 0, last_error: null } : device,
        );
        publish();
        return HttpResponse.json({ id: known.id }, { status: 201 });
      }
      if (store.devices.length >= DEVICE_LIMIT) {
        return refusal(409, 'refused', 'at most 16 push devices may be registered');
      }
      const id = store.next++;
      store.devices = [
        ...store.devices,
        { id, transport: 'webpush', endpoint, label, created_ts: now(), failure_count: 0, last_error: null },
      ];
      publish();
      return HttpResponse.json({ id }, { status: 201 });
    }),
    http.delete(`*${API_BASE}/devices/:id`, async ({ request, params }) => {
      scenario(read);
      await record(request);
      store.devices = store.devices.filter((device) => String(device.id) !== params.id);
      publish();
      return new HttpResponse(null, { status: 204 });
    }),
  ];
}

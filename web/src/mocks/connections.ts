import { delay, HttpResponse, http } from 'msw';
import type { ConnectionField, ConnectionView, Setting } from '../contract';
import { API_BASE } from '../contract';

export interface ConnectionsScenario {
  fail?: boolean;
  empty?: boolean;
  test?: 'pass' | 'fail' | 'error';
  saveTest?: 'fail';
  conflict?: boolean;
  settingsStale?: boolean;
  pinned?: boolean;
  sharedSlot?: boolean;
  pinnedRace?: boolean;
}

export interface ConnectionRequest {
  method: string;
  path: string;
  key: string | null;
  ifMatch: string | null;
  body: unknown;
}

const log = globalThis as typeof globalThis & { atriumConnectionRequests?: ConnectionRequest[] };
const EVENTS = 'atrium-mock-events';
const HOUR = 3600_000;

function iso(offset: number): string {
  return new Date(Date.now() + offset).toISOString();
}

function caps(...names: string[]) {
  return names.map((name) => ({ name, enabled: true, config: {} }));
}

function okResults(...names: string[]) {
  return names.map((capability) => ({ capability, state: 'ok' }));
}

const declaredSlots: Record<string, string[]> = {
  'service/anthropic': ['api_key'],
  'service/ship24': ['api_key'],
  'service/webpush': ['vapid_private_key'],
  'mail/imap': ['password'],
  'media/jellyfin': ['api_key'],
};

function seed(): ConnectionView[] {
  const rows: Omit<ConnectionView, 'dependents'>[] = [
    {
      id: 'service/anthropic',
      kind: 'service/anthropic',
      label: 'Anthropic',
      enabled: true,
      config_source: 'host',
      config: {},
      capabilities: caps('messages'),
      credentials: [{ slot: 'api_key', source: 'host_file', present: true, file_ref: '/run/secrets/anthropic-key' }],
      state: 'ok',
      reason: '',
      results: okResults('messages'),
      checked_at: iso(-2 * HOUR),
      last_ok_at: iso(-2 * HOUR),
    },
    {
      id: 'service/weather',
      kind: 'service/weather',
      label: 'Open-Meteo',
      enabled: true,
      config_source: 'host',
      config: {
        url: 'https://api.open-meteo.com/v1/forecast',
        geocoding_url: 'https://geocoding-api.open-meteo.com/v1/search',
      },
      capabilities: caps('forecast', 'geocoding'),
      credentials: [],
      state: 'ok',
      reason: '',
      results: okResults('forecast', 'geocoding'),
      checked_at: iso(-3 * HOUR),
      last_ok_at: iso(-3 * HOUR),
    },
    {
      id: 'service/ship24',
      kind: 'service/ship24',
      label: 'Ship24 parcels',
      enabled: true,
      config_source: 'operator',
      config: { url: 'https://parcels.example.test/v1' },
      capabilities: caps('tracking'),
      credentials: [
        {
          slot: 'api_key',
          source: 'database',
          present: true,
          set_at: iso(-30 * 24 * HOUR),
          set_by: 'operator@example.test',
        },
      ],
      state: 'ok',
      reason: '',
      results: okResults('tracking'),
      checked_at: iso(-HOUR),
      last_ok_at: iso(-HOUR),
    },
    {
      id: 'service/webpush',
      kind: 'service/webpush',
      label: 'Web push',
      enabled: true,
      config_source: 'operator',
      config: { subject: 'mailto:operator@example.test' },
      capabilities: caps('send'),
      credentials: [
        {
          slot: 'vapid_private_key',
          source: 'database',
          present: true,
          set_at: iso(-HOUR),
          set_by: 'operator@example.test',
        },
      ],
      state: 'untested',
      reason: '',
      results: [],
    },
    {
      id: 'media/qbittorrent',
      kind: 'media/qbittorrent',
      label: 'Downloads box',
      enabled: true,
      config_source: 'host',
      config: { url: 'http://downloads.example.test:8080' },
      capabilities: caps('downloads'),
      credentials: [],
      state: 'failing',
      reason: 'dial tcp 192.0.2.10:8080: connection refused',
      results: [
        {
          capability: 'downloads',
          state: 'failing',
          reason: 'dial tcp 192.0.2.10:8080: connection refused',
          grace_until: iso(4 * HOUR),
        },
      ],
      checked_at: iso(-HOUR / 2),
      last_ok_at: iso(-2 * HOUR),
      grace_until: iso(4 * HOUR),
    },
    {
      id: 'media/jellyfin',
      kind: 'media/jellyfin',
      label: 'Living room media',
      enabled: true,
      config_source: 'host',
      config: { url: 'http://media.example.test:8096' },
      capabilities: caps('library', 'playback'),
      credentials: [{ slot: 'api_key', source: 'host_env', present: true, file_ref: 'MEDIA_API_KEY' }],
      state: 'failing',
      reason: 'HTTP 401 from /System/Info',
      results: [
        { capability: 'library', state: 'failing', reason: 'HTTP 401 from /System/Info' },
        { capability: 'playback', state: 'failing', reason: 'HTTP 401 from /Sessions' },
      ],
      checked_at: iso(-HOUR / 4),
      last_ok_at: iso(-9 * 24 * HOUR),
    },
    {
      id: 'auth/oidc',
      kind: 'auth/oidc',
      label: 'Sign-in provider',
      enabled: true,
      config_source: 'host',
      config: { url: 'https://id.example.test/realms/home' },
      capabilities: caps('sign-in'),
      credentials: [],
      state: 'untested',
      reason: 'its test needs an interactive sign-in',
      results: [],
    },
    {
      id: 'google/calendar',
      kind: 'google/calendar',
      label: 'Family calendar',
      enabled: true,
      config_source: 'operator',
      config: {},
      capabilities: caps('calendar'),
      credentials: [
        { slot: 'refresh_token', source: 'database', present: true, set_at: iso(-60 * 24 * HOUR), set_by: 'oauth' },
      ],
      state: 'needs_reauth',
      reason: 'the refresh token was revoked',
      results: [{ capability: 'calendar', state: 'needs_reauth', reason: 'the refresh token was revoked' }],
      checked_at: iso(-5 * HOUR),
      last_ok_at: iso(-3 * 24 * HOUR),
      reauth_at: iso(-5 * HOUR),
    },
    {
      id: 'notion/workspace',
      kind: 'notion/workspace',
      label: 'Notes workspace',
      enabled: false,
      config_source: 'host',
      config: {},
      capabilities: [{ name: 'pages', enabled: false, config: {} }],
      credentials: [],
      state: 'disabled',
      reason: 'retired; the host file turns it off',
      results: [],
      checked_at: iso(-40 * 24 * HOUR),
    },
    {
      id: 'mail/garden-club',
      kind: 'mail/imap',
      label: 'Garden club mail',
      enabled: true,
      config_source: 'operator',
      config: { url: 'imaps://mail.example.test:993' },
      capabilities: caps('read', 'send'),
      credentials: [],
      state: 'unconfigured',
      reason: 'no password is stored',
      results: [],
    },
    {
      id: 'service/embedding',
      kind: 'service/embedding',
      label: 'Embeddings',
      enabled: true,
      config_source: 'host',
      config: { url: 'https://embed.example.test/v1' },
      capabilities: caps('embed'),
      credentials: [
        {
          slot: 'api_key',
          source: 'host_file',
          present: true,
          file_ref: '/run/secrets/embed-key',
          expires_at: iso(5 * 24 * HOUR),
        },
      ],
      state: 'expiring',
      reason: 'the API key expires in 5 days',
      results: [
        {
          capability: 'embed',
          state: 'expiring',
          reason: 'the API key expires in 5 days',
          expires_at: iso(5 * 24 * HOUR),
        },
      ],
      checked_at: iso(-6 * HOUR),
      last_ok_at: iso(-6 * HOUR),
      expires_at: iso(5 * 24 * HOUR),
    },
  ];
  return rows.map((row) => ({ ...row, dependents: [] }));
}

function sharedRows(): ConnectionView[] {
  const reason = 'the server refused the app password';
  const rows: Omit<ConnectionView, 'dependents'>[] = [
    {
      id: 'mail/harbor',
      kind: 'mail/imap',
      label: 'Harbor mail',
      enabled: true,
      config_source: 'host',
      config: { url: 'imaps://mail.example.test:993' },
      capabilities: caps('read'),
      credentials: [
        {
          slot: 'mail/harbor/password',
          source: 'host_file',
          present: true,
          pinned: true,
          file_ref: '/run/secrets/harbor-mail',
        },
      ],
      state: 'needs_reauth',
      reason,
      results: [{ capability: 'read', state: 'needs_reauth', reason }],
      checked_at: iso(-HOUR),
    },
    {
      id: 'icloud/harbor',
      kind: 'icloud',
      label: 'Harbor calendar',
      enabled: true,
      config_source: 'operator',
      config: { apple_id: 'harbor@example.test' },
      capabilities: caps('caldav'),
      credentials: [
        { slot: 'mail/harbor/password', source: 'host_file', present: true, pinned: true, owner: 'mail/harbor' },
      ],
      state: 'needs_reauth',
      reason,
      results: [{ capability: 'caldav', state: 'needs_reauth', reason }],
      checked_at: iso(-HOUR),
    },
  ];
  return rows.map((row) => ({ ...row, dependents: [] }));
}

interface Store {
  rows: ConnectionView[];
  settings: Record<string, string>;
  revision: number;
  staleUsed: boolean;
}

let store: Store = { rows: seed(), settings: {}, revision: 7, staleUsed: false };
let scenarioText = '';

function scenario(read: () => { connections?: ConnectionsScenario }): ConnectionsScenario {
  const state = read().connections ?? {};
  const text = JSON.stringify(state);
  if (text !== scenarioText) {
    scenarioText = text;
    store = {
      rows: state.empty ? [] : state.sharedSlot ? [...seed(), ...sharedRows()] : seed(),
      settings: { weather_location_name: 'Hillfield', weather_latitude: '51.48', weather_longitude: '-3.18' },
      revision: 7,
      staleUsed: false,
    };
  }
  return state;
}

export function weatherPlace(): string | undefined {
  return store.settings.weather_location_name;
}

function publish(topic: string): void {
  const channel = new BroadcastChannel(EVENTS);
  channel.postMessage({ topic });
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
  log.atriumConnectionRequests = [
    ...(log.atriumConnectionRequests ?? []),
    {
      method: request.method,
      path: new URL(request.url).pathname,
      key: request.headers.get('Idempotency-Key'),
      ifMatch: request.headers.get('If-Match'),
      body,
    },
  ];
  return body;
}

function refusal(status: number, code: string, message: string, extra: Record<string, unknown> = {}): Response {
  return HttpResponse.json({ error: { code, message }, ...extra }, { status });
}

function find(kind: unknown, name: unknown): ConnectionView | undefined {
  return store.rows.find((row) => row.id === `${kind}/${name}`);
}

function fields(row: ConnectionView): ConnectionField[] {
  const host = row.config_source === 'host';
  const config = row.config as Record<string, unknown>;
  const out: ConnectionField[] = [
    { name: 'label', type: 'text', secret: false, host_owned: host, present: row.label !== '' },
  ];
  if (typeof config.url === 'string') {
    out.push({ name: 'url', type: 'url', secret: false, host_owned: host, present: config.url !== '' });
  }
  for (const credential of row.credentials) {
    out.push({
      name: credential.slot,
      type: 'secret',
      secret: true,
      host_owned: credential.pinned === true || credential.source.startsWith('host'),
      present: credential.present,
      ...(credential.pinned ? { pinned: true } : {}),
      ...(credential.owner ? { owner: credential.owner } : {}),
    });
  }
  for (const slot of declaredSlots[row.kind] ?? []) {
    if (!row.credentials.some((credential) => credential.slot === slot)) {
      out.push({ name: slot, type: 'secret', secret: true, host_owned: false, present: false });
    }
  }
  return out;
}

interface FieldsBody {
  label?: string;
  url?: string;
  credentials?: Record<string, string>;
}

function apply(row: ConnectionView, body: FieldsBody): void {
  if (body.label !== undefined) row.label = body.label;
  if (body.url !== undefined) row.config = { ...(row.config as object), url: body.url };
  for (const slot of Object.keys(body.credentials ?? {})) {
    const others = row.credentials.filter((credential) => credential.slot !== slot);
    row.credentials = [
      ...others,
      { slot, source: 'database', present: true, set_at: new Date().toISOString(), set_by: 'operator@example.test' },
    ].sort((a, b) => a.slot.localeCompare(b.slot));
  }
}

function pass(row: ConnectionView): void {
  const now = new Date().toISOString();
  Object.assign(row, {
    state: 'ok',
    reason: '',
    checked_at: now,
    last_ok_at: now,
    grace_until: undefined,
    results: row.capabilities.filter((c) => c.enabled).map((c) => ({ capability: c.name, state: 'ok' })),
  });
}

function fail(row: ConnectionView, reason: string): void {
  Object.assign(row, {
    state: 'failing',
    reason,
    checked_at: new Date().toISOString(),
    grace_until: undefined,
    results: row.capabilities.filter((c) => c.enabled).map((c) => ({ capability: c.name, state: 'failing', reason })),
  });
}

function editable(row: ConnectionView, body: FieldsBody, state: ConnectionsScenario): Response | undefined {
  if (row.config_source === 'host' && (body.label !== undefined || body.url !== undefined)) {
    return refusal(
      422,
      'invalid',
      `connections: ${row.id} is declared in the host file; its label and url are read-only`,
    );
  }
  for (const slot of Object.keys(body.credentials ?? {})) {
    const held = row.credentials.find((credential) => credential.slot === slot);
    if (state.pinnedRace || held?.pinned)
      return refusal(
        409,
        'pinned',
        `connections: ${held?.owner ?? row.id} credential ${slot} is pinned by host configuration; change it there`,
      );
    if (held?.source.startsWith('host'))
      return refusal(422, 'invalid', `connections: ${row.id} ${slot} is provided by the host`);
  }
  return undefined;
}

function settingItems(state: ConnectionsScenario): Setting[] {
  return ['weather_location_name', 'weather_latitude', 'weather_longitude'].map((key) => {
    const value = store.settings[key];
    return {
      key,
      class: 'operator',
      value: value ?? null,
      source: value === undefined ? null : state.pinned && key === 'weather_location_name' ? 'pin' : 'db',
      ...(state.pinned && key === 'weather_location_name' ? { pin_source: 'host file' } : {}),
    };
  });
}

export function connectionHandlers(read: () => { connections?: ConnectionsScenario }) {
  return [
    http.get(`*${API_BASE}/connections`, () => {
      if (scenario(read).fail) return refusal(500, 'internal', 'The connection store did not answer.');
      return HttpResponse.json({ data: { connections: store.rows }, revision: '3' });
    }),
    http.get(`*${API_BASE}/connections/schema`, () => {
      scenario(read);
      return HttpResponse.json({
        kinds: Object.entries(declaredSlots).map(([kind, slots]) => ({
          kind,
          fields: [
            { name: 'label', type: 'text', secret: false, host_owned: false, present: false },
            ...slots.map((name) => ({ name, type: 'secret', secret: true, host_owned: false, present: false })),
          ],
        })),
        connections: store.rows.map((row) => ({ id: row.id, kind: row.kind, label: row.label, fields: fields(row) })),
      });
    }),
    http.post(`*${API_BASE}/connections/:kind/:name/test`, async ({ params, request }) => {
      const state = scenario(read);
      await record(request);
      await delay(400);
      const row = find(params.kind, params.name);
      if (!row) return refusal(404, 'not_found', 'No such connection.');
      if (!row.enabled) return refusal(409, 'refused', `connections: ${row.id} is disabled`);
      if (state.test === 'error') return refusal(500, 'internal', 'The test runner stopped before it answered.');
      if (state.test === 'fail') fail(row, 'dial tcp 192.0.2.44:443: i/o timeout');
      else pass(row);
      publish('connections');
      return HttpResponse.json({ data: { connections: [row] }, revision: '4' });
    }),
    http.put(`*${API_BASE}/connections/:kind/:name`, async ({ params, request }) => {
      const state = scenario(read);
      const body = ((await record(request)) ?? {}) as FieldsBody;
      await delay(300);
      const row = find(params.kind, params.name);
      if (!row) return refusal(404, 'not_found', 'No such connection.');
      if (state.conflict)
        return refusal(409, 'conflict', `connections: ${row.id} changed since revision 3; reload and try again`);
      const refused = editable(row, body, state);
      if (refused) return refused;
      if (state.saveTest === 'fail') {
        return refusal(422, 'invalid', 'test failed (failing): HTTP 401 from /v1/trackers');
      }
      apply(row, body);
      pass(row);
      publish('connections');
      return HttpResponse.json({ data: row, revision: '4' });
    }),
    http.patch(`*${API_BASE}/connections/:kind/:name`, async ({ params, request }) => {
      const state = scenario(read);
      const body = ((await record(request)) ?? {}) as FieldsBody;
      const row = find(params.kind, params.name);
      if (!row) return refusal(404, 'not_found', 'No such connection.');
      if (state.conflict)
        return refusal(409, 'conflict', `connections: ${row.id} changed since revision 3; reload and try again`);
      const refused = editable(row, body, state);
      if (refused) return refused;
      apply(row, body);
      Object.assign(row, { state: 'untested', reason: '', results: [], grace_until: undefined });
      publish('connections');
      return HttpResponse.json({ data: row, revision: '4' });
    }),
    http.delete(`*${API_BASE}/connections/:kind/:name`, async ({ params, request }) => {
      scenario(read);
      await record(request);
      await delay(300);
      const row = find(params.kind, params.name);
      if (!row) return refusal(404, 'not_found', 'No such connection.');
      Object.assign(row, {
        credentials: [],
        config: {},
        enabled: false,
        state: 'unconfigured',
        reason: '',
        results: [],
        checked_at: undefined,
        last_ok_at: undefined,
        expires_at: undefined,
        reauth_at: undefined,
        grace_until: undefined,
      });
      publish('connections');
      return new HttpResponse(null, { status: 204 });
    }),
    http.get(`*${API_BASE}/settings`, ({ request }) => {
      const state = scenario(read);
      const group = new URL(request.url).searchParams.get('group');
      const items = group === 'assistant' ? settingItems(state) : [];
      return HttpResponse.json({ revision: String(store.revision), data: { items, next_cursor: null } });
    }),
    http.patch(`*${API_BASE}/settings`, async ({ request }) => {
      const state = scenario(read);
      const body = ((await record(request)) ?? {}) as { set?: Record<string, string>; unset?: string[] };
      if (state.settingsStale && !store.staleUsed) {
        store.staleUsed = true;
        store.revision += 1;
        store.settings.weather_latitude = '51.5';
        publish('settings');
      }
      if (request.headers.get('If-Match') !== `W/"${store.revision}"`) {
        return refusal(412, 'precondition_failed', 'The settings changed since they were read.', {
          revision: String(store.revision),
        });
      }
      for (const [key, value] of Object.entries(body.set ?? {})) {
        if (state.pinned && key === 'weather_location_name') {
          return refusal(422, 'invalid', `${key} is pinned by the host file`);
        }
        if (key !== 'weather_location_name') {
          const limit = key === 'weather_latitude' ? 90 : 180;
          const number = Number(value);
          if (!Number.isFinite(number) || Math.abs(number) > limit) {
            return refusal(422, 'invalid', `${key} must be between -${limit} and ${limit}`);
          }
        }
      }
      Object.assign(store.settings, body.set ?? {});
      for (const key of body.unset ?? []) delete store.settings[key];
      store.revision += 1;
      publish('settings');
      return HttpResponse.json({
        data: { keys: [...Object.keys(body.set ?? {}), ...(body.unset ?? [])], revision: String(store.revision) },
      });
    }),
  ];
}

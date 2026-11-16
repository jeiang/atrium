import { delay, HttpResponse, http } from 'msw';
import schemaDocument from '../../../docs/api/settings-schema.json';
import type { BackupEntry, Setting, SettingsSchema, SettingsSchemaField } from '../contract';
import { API_BASE } from '../contract';

export interface SettingsScenario {
  fail?: boolean;
  empty?: boolean;
  slow?: boolean;
  pinned?: Record<string, string>;
  stored?: Record<string, string>;
  elsewhere?: Record<string, string>;
  refuse?: Record<string, string>;
  plant?: boolean;
  backups?: 'none' | 'unconfigured' | 'fail' | 'verify-fail';
}

export interface SettingsRequest {
  method: string;
  path: string;
  key: string | null;
  ifMatch: string | null;
  body: unknown;
}

export const PLANTED_KEY = 'planted_unplaced_key';
export const WEATHER_KEYS = ['weather_location_name', 'weather_latitude', 'weather_longitude'];

const log = globalThis as typeof globalThis & { atriumSettingsRequests?: SettingsRequest[] };
const EVENTS = 'atrium-mock-events';
const DAY = 86_400_000;
const document = schemaDocument as SettingsSchema;

interface Store {
  values: Record<string, string>;
  revision: number;
  elsewhereUsed: boolean;
  backups: BackupEntry[];
}

function seedBackups(): BackupEntry[] {
  const at = (days: number) => new Date(Date.now() - days * DAY).toISOString();
  const entry = (days: number, size: number, problem?: string): BackupEntry => ({
    name: `atrium-${at(days).slice(0, 10)}.tar.age`,
    size_bytes: size,
    modified_at: at(days),
    format: 2,
    header: problem
      ? null
      : { created_at: at(days), schema: 4, encryption: { encrypted: true, key_id: 'k1' }, files: 3 },
    ...(problem ? { problem } : {}),
  });
  return [entry(0.2, 48_211_000), entry(1.2, 47_904_000), entry(2.2, 47_650_000, 'old_format')];
}

function seed(state: SettingsScenario): Store {
  return {
    values: { mail_enabled: 'true', assistant_enabled: 'true', notifications_enabled: 'true', ...state.stored },
    revision: 11,
    elsewhereUsed: false,
    backups: state.backups === 'none' || state.backups === 'unconfigured' ? [] : seedBackups(),
  };
}

let store: Store = seed({});
let scenarioText = '';

function scenario(read: () => { settings?: SettingsScenario }): SettingsScenario {
  const state = read().settings ?? {};
  const text = JSON.stringify(state);
  if (text !== scenarioText) {
    scenarioText = text;
    store = seed(state);
  }
  return state;
}

function fields(state: SettingsScenario): SettingsSchemaField[] {
  const out = document.pages.flatMap((page) => page.fields);
  if (state.plant) out.push({ ...out[0], key: PLANTED_KEY, label: 'Planted', group: 'nowhere' } as SettingsSchemaField);
  return out;
}

function row(state: SettingsScenario, field: SettingsSchemaField): Setting {
  const pin = state.pinned?.[field.key];
  if (field.secret) return { key: field.key, class: field.class };
  if (pin !== undefined) {
    return {
      key: field.key,
      class: field.class,
      value: store.values[field.key] ?? field.default ?? '',
      source: 'pin',
      pin_source: pin,
    };
  }
  const stored = store.values[field.key];
  return {
    key: field.key,
    class: field.class,
    value: stored ?? field.default ?? null,
    source: stored === undefined ? 'default' : 'db',
  };
}

function schema(state: SettingsScenario): SettingsSchema {
  const pages: SettingsSchema['pages'] = document.pages.map((page) => ({
    ...page,
    fields: page.fields.map((field) => {
      const current = row(state, field);
      return {
        ...field,
        value: current.value ?? null,
        source: current.source ?? null,
        ...(current.pin_source ? { pin_source: current.pin_source, editable: false } : {}),
      };
    }),
  }));
  if (state.plant && pages[0]) {
    const planted = fields(state).at(-1);
    if (planted) pages[0] = { ...pages[0], fields: [...pages[0].fields, planted] };
  }
  return { pages };
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
  log.atriumSettingsRequests = [
    ...(log.atriumSettingsRequests ?? []),
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
  return HttpResponse.json({ error: { code, message, ...extra } }, { status });
}

function invalid(field: SettingsSchemaField, value: string): string | undefined {
  const text = value.trim();
  if (field.type === 'bool' && text !== 'true' && text !== 'false') return 'must be true or false';
  if (field.type === 'enum' && !field.choices.some((choice) => choice.value === text)) {
    return `must be one of ${field.choices.map((choice) => choice.value).join(', ')}`;
  }
  if (field.multi) {
    const bad = text
      .split(',')
      .find((part) => part.trim() && !field.choices.some((choice) => choice.value === part.trim()));
    if (bad) return `${bad} is not one of ${field.choices.map((choice) => choice.value).join(', ')}`;
  }
  if (field.type === 'int' || field.type === 'float') {
    const number = Number(text);
    if (text === '' || !Number.isFinite(number) || (field.type === 'int' && !Number.isInteger(number))) {
      return `invalid ${field.type === 'int' ? 'integer' : 'number'} ${JSON.stringify(value)}`;
    }
    if (number < 0) return 'must not be negative';
    if (field.min !== undefined && field.max !== undefined && (number < field.min || number > field.max)) {
      return `must be between ${field.min} and ${field.max}`;
    }
  }
  return undefined;
}

export function settingsHandlers(read: () => { settings?: SettingsScenario }) {
  return [
    http.get(`*${API_BASE}/settings/schema`, async () => {
      const state = scenario(read);
      if (state.slow) await delay(1500);
      if (state.fail) return refusal(500, 'internal', 'The settings store did not answer.');
      if (state.empty) return HttpResponse.json({ pages: [] });
      return HttpResponse.json(schema(state), { headers: { ETag: `W/"${store.revision}"` } });
    }),
    http.get(`*${API_BASE}/settings`, async ({ request }) => {
      const url = new URL(request.url);
      if (url.searchParams.has('group')) return undefined;
      const state = scenario(read);
      if (state.slow) await delay(1500);
      if (state.fail) return refusal(500, 'internal', 'The settings store did not answer.');
      const after = url.searchParams.get('cursor') ?? '';
      const limit = Number(url.searchParams.get('limit') ?? 50);
      const all = state.empty
        ? []
        : fields(state)
            .filter((field) => !WEATHER_KEYS.includes(field.key) && field.key > after)
            .sort((a, b) => a.key.localeCompare(b.key));
      const page = all.slice(0, limit);
      const next = all.length > limit ? (page.at(-1)?.key ?? null) : null;
      return HttpResponse.json({
        revision: String(store.revision),
        data: { items: page.map((field) => row(state, field)), next_cursor: next },
      });
    }),
    http.patch(`*${API_BASE}/settings`, async ({ request }) => {
      const body = (JSON.parse((await request.clone().text()) || '{}') ?? {}) as {
        set?: Record<string, string>;
        unset?: string[];
      };
      const named = [...Object.keys(body.set ?? {}), ...(body.unset ?? [])];
      if (named.length > 0 && named.every((key) => WEATHER_KEYS.includes(key))) return undefined;
      const state = scenario(read);
      await record(request);
      await delay(250);
      if (state.elsewhere && !store.elsewhereUsed) {
        store.elsewhereUsed = true;
        Object.assign(store.values, state.elsewhere);
        store.revision += 1;
        publish('settings');
      }
      if (request.headers.get('If-Match') !== `W/"${store.revision}"`) {
        return refusal(
          412,
          'precondition_failed',
          'The settings changed since they were read; reload them and try again.',
          {
            revision: String(store.revision),
          },
        );
      }
      const changes = [
        ...Object.entries(body.set ?? {}).map(([key, value]) => ({ key, value, unset: false })),
        ...(body.unset ?? []).map((key) => ({ key, value: '', unset: true })),
      ];
      if (changes.length === 0) return refusal(422, 'invalid', 'Name at least one setting to set or unset.');
      const problems: { type: string; loc: string[]; msg: string }[] = [];
      for (const change of changes) {
        const field = fields(state).find((item) => item.key === change.key);
        if (!field || WEATHER_KEYS.includes(change.key) || change.key === PLANTED_KEY) {
          return refusal(409, 'refused', `${change.key} is not offered on the settings pages and is not writable here`);
        }
        const pin = state.pinned?.[change.key];
        if (pin !== undefined) return refusal(409, 'refused', `${change.key} is pinned by ${pin}`);
        const message = state.refuse?.[change.key] ?? (change.unset ? undefined : invalid(field, change.value));
        if (message) problems.push({ type: 'invalid', loc: ['body', change.key], msg: `${change.key}: ${message}` });
      }
      if (problems.length > 0) {
        return refusal(422, 'invalid', problems.map((problem) => problem.msg).join('; '), { fields: problems });
      }
      for (const change of changes) {
        if (change.unset || change.value.trim() === '') delete store.values[change.key];
        else store.values[change.key] = change.value.trim();
      }
      store.revision += 1;
      publish('settings');
      return HttpResponse.json({
        data: { keys: changes.map((change) => change.key), revision: String(store.revision) },
        audit_id: 900 + store.revision,
      });
    }),
    http.get(`*${API_BASE}/maintenance/backups`, async () => {
      const state = scenario(read);
      if (state.backups === 'fail') return refusal(500, 'internal', 'The backup folder could not be read.');
      return HttpResponse.json({
        revision: String(store.backups.length),
        data: {
          location: {
            state: state.backups === 'unconfigured' ? 'unconfigured' : 'ready',
            host_key: 'core.backup_directory',
            nix_option: 'services.atrium.backup.directory',
          },
          items: store.backups,
          total: store.backups.length,
        },
      });
    }),
    http.post(`*${API_BASE}/maintenance/backups/verify`, async ({ request }) => {
      const state = scenario(read);
      const body = (await record(request)) as { name?: string } | undefined;
      await delay(500);
      const entry = store.backups.find((item) => item.name === body?.name);
      if (!entry) return refusal(404, 'not_found', 'No such archive.');
      if (state.backups === 'verify-fail' || entry.problem) {
        return refusal(422, 'invalid', `${entry.name}: ${entry.problem ?? 'the archive key does not open it'}`);
      }
      return HttpResponse.json({
        data: {
          name: entry.name,
          created_at: entry.modified_at,
          encryption: { encrypted: true, key_id: 'k1' },
          key_ids: ['k1'],
          from_schema: 4,
          to_schema: 4,
          tables: [],
          knn: [],
          files: 3,
        },
        audit_id: 800 + store.backups.length,
        effect_ids: [],
        undoable: false,
      });
    }),
    http.post(`*${API_BASE}/maintenance/backups`, async ({ request }) => {
      scenario(read);
      await record(request);
      await delay(700);
      const now = new Date().toISOString();
      const entry: BackupEntry = {
        name: `atrium-${now.slice(0, 19).replaceAll(':', '')}.tar.age`,
        size_bytes: 48_300_000,
        modified_at: now,
        format: 2,
        header: { created_at: now, schema: 4, encryption: { encrypted: true, key_id: 'k1' }, files: 3 },
      };
      store.backups = [entry, ...store.backups];
      publish('settings');
      return HttpResponse.json({
        data: { name: entry.name, encryption: { encrypted: true, key_id: 'k1' }, files: 3 },
        audit_id: 700 + store.backups.length,
        effect_ids: [],
        undoable: false,
      });
    }),
  ];
}

import { createQuery, type QueryClient } from '@tanstack/svelte-query';
import {
  ApiError,
  type ConnectionField,
  type ConnectionKindSchema,
  type ConnectionSchema,
  type ConnectionView,
  connectionsList,
  connectionsOauthSetup,
  connectionsSchema,
  ok,
  type Setting,
  settingsRead,
} from '../../contract';
import type { Tone } from '../../shared/ui/Badge.svelte';

const CONNECTION_TOPICS = ['connections'] as const;
const SETTINGS_TOPICS = ['settings'] as const;

export const WEATHER_ID = 'service/weather';
export const GOOGLE_ID = 'google/default';
export const INTEGRATIONS_DOC = 'https://github.com/jeiang/atrium/blob/main/docs/integrations.md';
export const WEATHER_KEYS = {
  name: 'weather_location_name',
  latitude: 'weather_latitude',
  longitude: 'weather_longitude',
} as const;

export interface StateWords {
  label: string;
  tone: Tone;
}

const stateWords: Record<string, StateWords> = {
  ok: { label: 'OK', tone: 'ok' },
  failing: { label: 'Failing', tone: 'error' },
  untested: { label: 'Untested', tone: 'neutral' },
  disabled: { label: 'Disabled', tone: 'neutral' },
  unconfigured: { label: 'Not set up', tone: 'warn' },
  expiring: { label: 'Expiring', tone: 'warn' },
  needs_reauth: { label: 'Needs sign-in', tone: 'error' },
};

const clock = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });
const stamp = new Intl.DateTimeFormat(undefined, {
  day: 'numeric',
  month: 'short',
  year: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  hourCycle: 'h23',
});

export function when(value: string | undefined): string {
  return value ? stamp.format(new Date(value)) : 'Never';
}

export function words(state: string, graceUntil?: string): StateWords {
  if (state === 'failing' && graceUntil) {
    return { label: `Away until ${clock.format(new Date(graceUntil))}`, tone: 'warn' };
  }
  return stateWords[state] ?? { label: state.replaceAll('_', ' '), tone: 'neutral' };
}

const fallbackReasons: Record<string, string> = {
  ok: 'Every capability passed its last test.',
  untested: 'No capability has been tested yet.',
  disabled: 'Turned off.',
  unconfigured: 'It lacks what it needs to run.',
};

export function reasonText(view: Pick<ConnectionView, 'state' | 'reason'>): string {
  return view.reason || fallbackReasons[view.state] || '';
}

export function graceText(graceUntil: string): string {
  return `Failing since the last check. atrium keeps using it until ${when(graceUntil)} before it counts as failing.`;
}

export function ownerText(view: Pick<ConnectionView, 'config_source'>): string {
  return view.config_source === 'host' ? 'Host' : 'You';
}

export function hostSlot(source: string): boolean {
  return source.startsWith('host');
}

export function uses(view: Pick<ConnectionView, 'capabilities'>): string {
  const on = view.capabilities.filter((capability) => capability.enabled).map((capability) => capability.name);
  return on.length > 0 ? on.join(', ') : 'Nothing';
}

export function splitId(id: string): { kind: string; name: string } {
  const at = id.indexOf('/');
  return at < 0 ? { kind: id, name: '' } : { kind: id.slice(0, at), name: id.slice(at + 1) };
}

export function slotLabel(slot: string): string {
  const text = slot.replaceAll('_', ' ').replace(/\b(api|url|vapid)\b/g, (word) => word.toUpperCase());
  return text.charAt(0).toUpperCase() + text.slice(1);
}

export function configEntries(config: unknown): [string, string][] {
  if (typeof config !== 'object' || config === null || Array.isArray(config)) return [];
  return Object.entries(config as Record<string, unknown>).map(([key, value]) => [
    key,
    typeof value === 'string' ? value : JSON.stringify(value),
  ]);
}

export function normalized(view: ConnectionView): ConnectionView {
  return {
    ...view,
    capabilities: view.capabilities ?? [],
    credentials: view.credentials ?? [],
    results: view.results ?? [],
  };
}

export function useConnections() {
  return createQuery(() => ({
    queryKey: ['connections', 'list'],
    queryFn: async ({ signal }) => ok(await connectionsList({ signal })).data.connections.map(normalized),
    meta: { topics: CONNECTION_TOPICS },
  }));
}

export function useSchema() {
  return createQuery(() => ({
    queryKey: ['connections', 'schema'],
    queryFn: async ({ signal }) => ok(await connectionsSchema({ signal })),
    meta: { topics: CONNECTION_TOPICS },
  }));
}

export function useGoogleSetup() {
  return createQuery(() => ({
    queryKey: ['connections', 'google'],
    queryFn: async ({ signal }) => ok(await connectionsOauthSetup({ signal })).data,
    meta: { topics: CONNECTION_TOPICS },
  }));
}

export interface WeatherSettings {
  revision: string;
  items: Record<string, Setting | undefined>;
}

export function useWeatherSettings(enabled: () => boolean) {
  return createQuery(() => ({
    queryKey: ['settings', 'weather'],
    enabled: enabled(),
    queryFn: async ({ signal }): Promise<WeatherSettings> => {
      const read = ok(await settingsRead({ group: 'assistant', limit: 100 }, { signal }));
      const items: Record<string, Setting | undefined> = {};
      for (const item of read.data.items) {
        if (Object.values(WEATHER_KEYS).includes(item.key as never)) items[item.key] = item;
      }
      return { revision: read.revision, items };
    },
    meta: { topics: SETTINGS_TOPICS },
  }));
}

export function fieldsFor(schema: { connections: ConnectionSchema[] } | undefined, id: string): ConnectionField[] {
  return schema?.connections.find((item) => item.id === id)?.fields ?? [];
}

export function guideFor(schema: { kinds: ConnectionKindSchema[] } | undefined, kind: string): string | undefined {
  const anchor = schema?.kinds.find((item) => item.kind === kind)?.docs;
  return anchor ? `${INTEGRATIONS_DOC}#${anchor}` : undefined;
}

export function gesture(): RequestInit {
  return { headers: { 'Idempotency-Key': crypto.randomUUID() } };
}

export interface Draft {
  label?: string;
  url?: string;
  credentials: Record<string, string>;
}

export function fieldsBody(draft: Draft) {
  const credentials = Object.fromEntries(Object.entries(draft.credentials).filter(([, value]) => value !== ''));
  return {
    ...(draft.label !== undefined ? { label: draft.label } : {}),
    ...(draft.url !== undefined ? { url: draft.url } : {}),
    ...(Object.keys(credentials).length > 0 ? { credentials } : {}),
  };
}

export function stale(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 409 || error.status === 412) && error.code !== 'refused';
}

export interface RepairHint {
  owner?: string;
  pinned: boolean;
}

export function repairHint(view: ConnectionView | undefined): RepairHint | undefined {
  const credential = view?.credentials.find((item) => item.owner || item.pinned);
  if (!credential) return undefined;
  return { owner: credential.owner, pinned: credential.pinned ?? false };
}

export function remember(client: QueryClient, view: ConnectionView): void {
  client.setQueryData<ConnectionView[]>(['connections', 'list'], (rows) =>
    rows?.map((row) => (row.id === view.id ? view : row)),
  );
}

export function refreshConnections(client: QueryClient): void {
  void client.invalidateQueries({ queryKey: ['connections'] });
}

export function refreshSettings(client: QueryClient): void {
  void client.invalidateQueries({ predicate: (query) => query.meta?.topics?.includes('settings') ?? false });
}

export function coordinate(limit: number, word: string): (value: string) => string | undefined {
  return (value) => {
    const text = value.trim();
    if (text === '') return undefined;
    const number = Number(text);
    if (!Number.isFinite(number)) return `Enter the ${word} as a number, such as ${limit === 90 ? '10.65' : '-61.5'}.`;
    if (number < -limit || number > limit) return `The ${word} must be between -${limit} and ${limit}.`;
    return undefined;
  };
}

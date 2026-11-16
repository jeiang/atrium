import { createQuery, type QueryClient } from '@tanstack/svelte-query';
import {
  maintenanceBackupList,
  ok,
  type Setting,
  type SettingsSchemaField,
  settingsRead,
  settingsSchema,
} from '../../contract';
import { appById } from '../../shell/nav';

const TOPICS = ['settings'] as const;

export interface SettingsValues {
  revision: string;
  items: Record<string, Setting | undefined>;
}

export function useSettingsSchema() {
  return createQuery(() => ({
    queryKey: ['settings', 'schema'],
    queryFn: async ({ signal }) => ok(await settingsSchema({ signal })),
    meta: { topics: TOPICS },
  }));
}

export function useSettingsValues() {
  return createQuery(() => ({
    queryKey: ['settings', 'values'],
    queryFn: async ({ signal }): Promise<SettingsValues> => {
      const items: Record<string, Setting | undefined> = {};
      let revision = '';
      let cursor: string | undefined;
      do {
        const page = ok(await settingsRead({ limit: 100, ...(cursor ? { cursor } : {}) }, { signal }));
        revision = page.revision;
        for (const item of page.data.items) items[item.key] = item;
        cursor = page.data.next_cursor ?? undefined;
      } while (cursor);
      return { revision, items };
    },
    meta: { topics: TOPICS },
  }));
}

export function useBackups() {
  return createQuery(() => ({
    queryKey: ['settings', 'backups'],
    queryFn: async ({ signal }) => ok(await maintenanceBackupList(undefined, { signal })),
    meta: { topics: TOPICS },
  }));
}

export function refreshSettings(client: QueryClient): void {
  void client.invalidateQueries({ predicate: (query) => query.meta?.topics?.includes('settings') ?? false });
}

export type Format = 'time-range' | 'weekdays';

export const FORMATS: Record<string, Format | undefined> = { work_hours: 'time-range', work_days: 'weekdays' };

export const WEEKDAYS = [
  { value: 'mon', label: 'Monday' },
  { value: 'tue', label: 'Tuesday' },
  { value: 'wed', label: 'Wednesday' },
  { value: 'thu', label: 'Thursday' },
  { value: 'fri', label: 'Friday' },
  { value: 'sat', label: 'Saturday' },
  { value: 'sun', label: 'Sunday' },
] as const;

export function unitOf(field: SettingsSchemaField): string | undefined {
  return /\(([^)]+)\)\s*$/.exec(field.label)?.[1];
}

export function labelOf(field: SettingsSchemaField): string {
  if (field.label !== field.key) return field.label;
  if (field.key.endsWith('_enabled')) {
    return appById(field.module)?.title ?? field.module.charAt(0).toUpperCase() + field.module.slice(1);
  }
  return field.key.replaceAll('_', ' ');
}

export function durationText(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '';
  if (seconds === 0) return 'no time';
  const parts: string[] = [];
  const units: [number, string][] = [
    [86_400, 'day'],
    [3600, 'hour'],
    [60, 'minute'],
    [1, 'second'],
  ];
  let rest = Math.round(seconds);
  for (const [size, word] of units) {
    const count = Math.floor(rest / size);
    rest -= count * size;
    if (count > 0) parts.push(`${count} ${word}${count === 1 ? '' : 's'}`);
    if (parts.length === 2) break;
  }
  return parts.join(' ');
}

const RANGE = /^([01]\d|2[0-3]):([0-5]\d)-([01]\d|2[0-3]):([0-5]\d)$/;

export function splitRange(value: string): [string, string] {
  const match = RANGE.exec(value.trim());
  return match ? [`${match[1]}:${match[2]}`, `${match[3]}:${match[4]}`] : ['', ''];
}

export function rangeText(value: string): string {
  const match = RANGE.exec(value.trim());
  if (!match) return '';
  const start = Number(match[1]) * 60 + Number(match[2]);
  const end = Number(match[3]) * 60 + Number(match[4]);
  const minutes = end > start ? end - start : end + 1440 - start;
  return `${match[1]}:${match[2]} to ${match[3]}:${match[4]}${end <= start ? ' the next day' : ''}, ${durationText(minutes * 60)}`;
}

export function pinText(pin: string): string {
  if (pin === '') return 'Set by the host';
  if (pin.startsWith('env ')) return `Set by the host in the environment variable ${pin.slice(4)}`;
  if (pin.startsWith('host ')) {
    const [path, name] = pin.slice(5).split(' ');
    return name ? `Set by the host in ${path} as ${name}` : `Set by the host in ${path}`;
  }
  return `Set by the host (${pin})`;
}

export function shownValue(field: SettingsSchemaField, value: string): string {
  if (value === '') return 'Not set';
  if (field.type === 'bool') return value === 'true' ? 'On' : 'Off';
  const choices = field.multi || field.type === 'enum' ? field.choices : [];
  if (FORMATS[field.key] === 'weekdays' || choices.length > 0) {
    const list = FORMATS[field.key] === 'weekdays' ? WEEKDAYS : choices;
    return value
      .split(',')
      .map((part) => list.find((choice) => choice.value === part.trim())?.label ?? part.trim())
      .join(', ');
  }
  const unit = unitOf(field);
  return unit ? `${value} ${unit}` : value;
}

export function checkValue(field: SettingsSchemaField, value: string): string | undefined {
  const text = value.trim();
  if (text === '') return undefined;
  if (field.type === 'int' || field.type === 'float') {
    const number = Number(text);
    if (!Number.isFinite(number)) return 'Enter a number.';
    if (field.type === 'int' && !Number.isInteger(number)) return 'Enter a whole number.';
    if (field.min != null && number < field.min) return `Enter ${field.min} or more.`;
    if (field.max != null && number > field.max) return `Enter ${field.max} or less.`;
  }
  if (FORMATS[field.key] === 'time-range' && !RANGE.test(text)) return 'Enter a start and an end time.';
  return undefined;
}

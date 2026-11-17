import { createQuery, type QueryClient } from '@tanstack/svelte-query';
import {
  coreAbout,
  maintenanceBackupList,
  ok,
  type Setting,
  type SettingsSchemaField,
  settingsRead,
  settingsSchema,
} from '../../contract';

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

export function useAbout() {
  return createQuery(() => ({
    queryKey: ['settings', 'about'],
    queryFn: async ({ signal }) => ok(await coreAbout({ signal })),
  }));
}

export function refreshSettings(client: QueryClient): void {
  void client.invalidateQueries({ predicate: (query) => query.meta?.topics?.includes('settings') ?? false });
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
  if (field.choices.length > 0) {
    return value
      .split(',')
      .map((part) => field.choices.find((choice) => choice.value === part.trim())?.label ?? part.trim())
      .join(', ');
  }
  return value;
}

export function checkValue(field: SettingsSchemaField, value: string): string | undefined {
  const text = value.trim();
  if (text === '' || (field.type !== 'int' && field.type !== 'float')) return undefined;
  const number = Number(text);
  if (!Number.isFinite(number)) return 'Enter a number.';
  if (field.type === 'int' && !Number.isInteger(number)) return 'Enter a whole number.';
  if (field.min != null && number < field.min) return `Enter ${field.min} or more.`;
  if (field.max != null && number > field.max) return `Enter ${field.max} or less.`;
  return undefined;
}

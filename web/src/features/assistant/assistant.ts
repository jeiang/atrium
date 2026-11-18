import { createQuery, type QueryClient } from '@tanstack/svelte-query';
import {
  ApiError,
  assistantConfiguration,
  assistantScheduleHistory,
  assistantScheduleList,
  assistantSpend,
  assistantSubagentRoles,
  ok,
  reviewPermissions,
  reviewProposals,
} from '../../contract';
import { refusedOffline } from '../../shared/device.svelte';

const SPEND_TOPICS = ['assistant', 'settings'] as const;
const JOB_TOPICS = ['jobs'] as const;
const PERMISSION_TOPICS = ['settings', 'review'] as const;

export const PERMISSION_REQUEST_LIMIT = 8;
export const HISTORY_LIMIT = 20;

export type AssistantView = 'behavior' | 'authority' | 'spend' | 'routines';

export type Tone = 'ok' | 'warn' | 'error' | 'info';

export interface Outcome {
  tone: Tone;
  title: string;
  text: string;
}

export function useConfiguration(enabled: () => boolean = () => true) {
  return createQuery(() => ({
    queryKey: ['assistant', 'configuration'],
    queryFn: async ({ signal }) => ok(await assistantConfiguration({ signal })),
    meta: { topics: SPEND_TOPICS },
    enabled: enabled(),
  }));
}

export function useRoles() {
  return createQuery(() => ({
    queryKey: ['assistant', 'roles'],
    queryFn: async ({ signal }) => ok(await assistantSubagentRoles({ signal })).items,
    meta: { topics: ['assistant'] },
  }));
}

export function useSpend() {
  return createQuery(() => ({
    queryKey: ['assistant', 'spend'],
    queryFn: async ({ signal }) => ok(await assistantSpend({ signal })),
    meta: { topics: SPEND_TOPICS },
  }));
}

export function usePermissions() {
  return createQuery(() => ({
    queryKey: ['assistant', 'permissions'],
    queryFn: async ({ signal }) => ok(await reviewPermissions({ signal })),
    meta: { topics: PERMISSION_TOPICS },
  }));
}

export function usePermissionRequests() {
  return createQuery(() => ({
    queryKey: ['assistant', 'permission-requests'],
    queryFn: async ({ signal }) =>
      ok(await reviewProposals({ kind: 'permission', limit: PERMISSION_REQUEST_LIMIT }, { signal })).data.items,
    meta: { topics: ['review', 'effects'] },
  }));
}

export function useSchedules() {
  return createQuery(() => ({
    queryKey: ['assistant', 'schedules'],
    queryFn: async ({ signal }) => ok(await assistantScheduleList({ limit: 100 }, { signal })),
    meta: { topics: JOB_TOPICS },
  }));
}

export function useScheduleRuns(id: () => number | undefined) {
  return createQuery(() => ({
    queryKey: ['assistant', 'schedule-runs', id()],
    enabled: id() !== undefined,
    queryFn: async ({ signal }) =>
      ok(await assistantScheduleHistory(id() ?? 0, { limit: HISTORY_LIMIT }, { signal })).items,
    meta: { topics: JOB_TOPICS },
  }));
}

export function gesture(revision?: string): RequestInit {
  const headers: Record<string, string> = { 'Idempotency-Key': crypto.randomUUID() };
  if (revision) headers['If-Match'] = `W/"${revision}"`;
  return { headers };
}

export function stale(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 409 || error.status === 412) && error.code !== 'refused';
}

export function fieldErrors(error: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  if (!(error instanceof ApiError)) return out;
  const body = error.body;
  if (typeof body !== 'object' || body === null || !('error' in body)) return out;
  const envelope = body.error;
  if (typeof envelope !== 'object' || envelope === null || !('fields' in envelope)) return out;
  const fields = envelope.fields;
  if (!Array.isArray(fields)) return out;
  for (const field of fields) {
    if (typeof field !== 'object' || field === null || !('loc' in field) || !('msg' in field)) continue;
    const key = Array.isArray(field.loc) ? field.loc.at(-1) : undefined;
    const msg = typeof field.msg === 'string' ? field.msg : '';
    if (typeof key !== 'string' || msg === '') continue;
    out[key] = msg.startsWith(`${key}: `) ? msg.slice(key.length + 2) : msg;
  }
  return out;
}

export function failure(error: unknown, what: string, refetch: () => void): Outcome {
  if (refusedOffline(error) && error instanceof Error) {
    return { tone: 'warn', title: 'Cannot reach atrium', text: error.message };
  }
  if (stale(error)) {
    refetch();
    return {
      tone: 'warn',
      title: 'This changed since it loaded',
      text: 'atrium reloaded it and kept your edits. Save again to replace the newer values.',
    };
  }
  if (error instanceof ApiError) {
    return { tone: 'error', title: `atrium refused ${what}`, text: `${error.message} Nothing was saved.` };
  }
  refetch();
  return {
    tone: 'error',
    title: 'The outcome is unknown',
    text: 'The connection failed before atrium answered. atrium reloaded the page data and did not try again.',
  };
}

export function refresh(client: QueryClient, topic: string): void {
  void client.invalidateQueries({ predicate: (query) => query.meta?.topics?.includes(topic) ?? false });
}

const usd = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 2 });
const smallUsd = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 4 });
const stamp = new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
const day = new Intl.DateTimeFormat('en-US', { month: 'long', day: 'numeric' });

export function money(value: number): string {
  return Math.abs(value) > 0 && Math.abs(value) < 0.01 ? smallUsd.format(value) : usd.format(value);
}

export function percent(share: number): string {
  return `${Math.round(share * 100)}%`;
}

export function dateTime(value: number | string | null | undefined): string {
  if (value === null || value === undefined || value === '') return '';
  const date = typeof value === 'number' ? new Date(value * 1000) : new Date(value);
  return Number.isNaN(date.getTime()) ? '' : stamp.format(date);
}

export function dayOf(value: number): string {
  return day.format(new Date(value * 1000));
}

export const kindWords: Record<string, string> = {
  chat: 'Chats',
  cron: 'Scheduled runs',
  nightly: 'Nightly run',
  sub: 'Research helpers',
  worker: 'Server ops',
  search: 'Web search',
};

export const classWords: Record<string, string> = {
  interactive: 'Chat',
  unattended: 'Scheduled and background work',
};

export function words(map: Record<string, string>, key: string): string {
  return map[key] ?? key.replaceAll('_', ' ');
}

export const runStatusWords: Record<string, { label: string; tone: 'ok' | 'warn' | 'error' | 'neutral' | 'info' }> = {
  ok: { label: 'Finished', tone: 'ok' },
  done: { label: 'Finished', tone: 'ok' },
  completed: { label: 'Finished', tone: 'ok' },
  running: { label: 'Running', tone: 'info' },
  requested: { label: 'Starting', tone: 'info' },
  error: { label: 'Failed', tone: 'error' },
  failed: { label: 'Failed', tone: 'error' },
  skipped_paused: { label: 'Skipped: paused', tone: 'warn' },
  skipped_module_off: { label: 'Skipped: app off', tone: 'warn' },
  skipped_budget: { label: 'Skipped: budget', tone: 'warn' },
  skipped_running: { label: 'Skipped: still running', tone: 'warn' },
};

export function runStatus(status: string | null | undefined) {
  if (!status) return undefined;
  return runStatusWords[status] ?? { label: status.replaceAll('_', ' '), tone: 'neutral' as const };
}
